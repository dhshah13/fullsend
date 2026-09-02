package runtime

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// Integrity anchors for the runner-owned files under CODEX_HOME.
//
// Everything Bootstrap writes there is agent-writable between iterations — the
// agent runs as the same user — so a guard is only worth as much as the place
// its expected value comes from. There are exactly two trustworthy places:
//
//  1. **This binary.** The hook adapter, the auth script and the shared hook
//     scripts are `go:embed`ed, so their SHA-256 is a compile-time constant
//     that the run command carries as a literal. Nothing in the sandbox can
//     change what the guard compares against.
//
//  2. **The runner's own memory.** config.toml and hooks.json are rendered per
//     run, so no compile-time constant exists for them. Bootstrap records what
//     it uploaded here, in the host process, and Run reads it back — the
//     runner calls both within one invocation (internal/cli/run.go).
//
// The manifest is deliberately *not* one of those places. It is a file in the
// same agent-writable directory, so an agent that rewrote config.toml could
// rewrite a hash recorded there to match, and the guard would pass. That is
// not a theoretical gap: a `projects."<repo>".trust_level = "trusted"` line in
// config.toml makes codex load the target repo's own `.codex/config.toml`,
// which can then set `developer_instructions`, `model` or repo-authored hooks —
// verified against codex 0.152.1, where the repo layer applied only with that
// line present. The manifest stays what it always was: information for Run and
// for an operator reading the sandbox, never authority.

// codexUploadedHashes are the SHA-256 digests of the per-run files Bootstrap
// uploaded. HooksJSON is empty when the harness has security disabled.
type codexUploadedHashes struct {
	ConfigTOML string
	HooksJSON  string
}

// codexArtifactHashes maps a sandbox name to the digests Bootstrap recorded.
// Process-local on purpose (see above); a sync.Map because the runner may
// bootstrap and run more than one sandbox concurrently.
var codexArtifactHashes sync.Map

func recordCodexArtifactHashes(sandboxName string, h codexUploadedHashes) {
	codexArtifactHashes.Store(sandboxName, h)
}

// lookupCodexArtifactHashes returns what Bootstrap recorded for this sandbox.
// A miss is fail-closed at the call site rather than a fallback to the
// manifest: falling back would reintroduce the agent-writable anchor this
// exists to avoid.
func lookupCodexArtifactHashes(sandboxName string) (codexUploadedHashes, bool) {
	v, ok := codexArtifactHashes.Load(sandboxName)
	if !ok {
		return codexUploadedHashes{}, false
	}
	h, ok := v.(codexUploadedHashes)
	return h, ok
}

// forgetCodexArtifactHashes drops a sandbox's entry; tests only.
func forgetCodexArtifactHashes(sandboxName string) {
	codexArtifactHashes.Delete(sandboxName)
}

// codexHookScriptDigests returns the SHA-256 of every hook script this binary
// can install, sorted for a stable guard string.
//
// The set that is actually installed depends on the harness, but each script's
// *bytes* are embedded here, so the guard can require that every script present
// in the hooks directory matches one of these without knowing which the harness
// enabled. An edited or agent-added script fails; deleting one narrows the
// wiring, which the hooks.json digest then catches.
func codexHookScriptDigests() []string {
	// The zero config yields the default-enabled set; the one opt-in script
	// (tool_allowlist_pretool.py) is asked for explicitly so the list covers
	// every script this binary can install, whatever the harness enabled.
	on := true
	files := security.HookFiles(security.SandboxHookConfigFromHarness(&harness.Harness{
		Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
			ToolAllowlistPreTool: &harness.ToolAllowlistConfig{Enabled: &on},
		}},
	}))
	seen := map[string]bool{}
	var digests []string
	for _, content := range files {
		sum := codexAssetSHA256(content)
		if !seen[sum] {
			seen[sum] = true
			digests = append(digests, sum)
		}
	}
	sort.Strings(digests)
	return digests
}

// codexHookScriptsGuard is the POSIX sh fragment that refuses to run when any
// file in the hooks directory is not one of this binary's own hook scripts.
//
// It closes the "the hook scripts are agent-writable between iterations" gap
// for codex: an agent that rewrites tirith_check.py to exit 0, or drops in a
// script of its own for the adapter to run, fails the run instead of quietly
// disabling a control. The comparison values are compile-time constants, so
// nothing in the sandbox contributes to the answer.
func codexHookScriptsGuard(hooksDir string) string {
	digests := codexHookScriptDigests()
	return fmt.Sprintf(
		`{ for f in %s/*.py; do test -e "$f" || continue; `+
			`h=$(command -p sha256sum "$f" | command -p cut -d' ' -f1); `+
			`case "$h" in %s) ;; *) echo "fullsend: sandbox hook script $f is not the copy fullsend installed; refusing to run" >&2; exit %d ;; esac; done; }`,
		shellQuote(hooksDir), strings.Join(digests, "|"), codexHooksMissingExit)
}
