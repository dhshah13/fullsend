package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/security"
)

// codexAdapterHarness lays the embedded adapter out the way Bootstrap does —
// the adapter at the top of a config dir, the hook scripts in hooks/ beside it
// — and runs it as codex would.
type codexAdapterHarness struct {
	t        *testing.T
	python   string
	dir      string
	hooksDir string
	adapter  string
}

func newCodexAdapterHarness(t *testing.T) *codexAdapterHarness {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	adapter := filepath.Join(dir, codexAdapterFile)
	require.NoError(t, os.WriteFile(adapter, codexHookAdapterPy, 0o755))
	return &codexAdapterHarness{t: t, python: python, dir: dir, hooksDir: hooksDir, adapter: adapter}
}

// script writes a fake hook script. body is Python executed with the decoded
// stdin payload bound to `payload`; it may print and call sys.exit.
func (h *codexAdapterHarness) script(name, body string) string {
	h.t.Helper()
	src := "import json, sys\npayload = json.load(sys.stdin)\n" + body + "\n"
	require.NoError(h.t, os.WriteFile(filepath.Join(h.hooksDir, name), []byte(src), 0o755))
	return name
}

type codexAdapterResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func (h *codexAdapterHarness) run(phase string, input map[string]any, scripts ...string) codexAdapterResult {
	h.t.Helper()
	payload, err := json.Marshal(input)
	require.NoError(h.t, err)

	args := append([]string{h.adapter, phase}, scripts...)
	cmd := exec.Command(h.python, args...)
	cmd.Stdin = strings.NewReader(string(payload))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		require.ErrorAs(h.t, runErr, &exitErr, "adapter failed to run: %s", stderr.String())
		exitCode = exitErr.ExitCode()
	}
	return codexAdapterResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func codexBashInput(command string) map[string]any {
	return map[string]any{
		"session_id":      "s1",
		"turn_id":         "t1",
		"cwd":             "/sandbox/workspace/repo",
		"hook_event_name": "PreToolUse",
		"model":           "gpt-5.6-luna",
		"permission_mode": "bypass",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": command},
		"tool_use_id":     "call_1",
	}
}

func TestCodexAdapter_PreToolUseAllow(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("allow.py", "sys.exit(0)")

	got := h.run("PreToolUse", codexBashInput("ls"), "allow.py")
	assert.Equal(t, 0, got.exitCode)
	assert.Empty(t, got.stdout, "an allow must write nothing: any stdout codex cannot parse makes the hook Failed")
	assert.Empty(t, got.stderr)
}

// TestCodexAdapter_PreToolUseBlockUsesExitTwo is the load-bearing translation.
// The hook scripts block with exit 1 plus a JSON decision, but codex treats any
// exit other than 0 and 2 as `Failed`, and a failed hook does not block — so
// forwarding exit 1 verbatim would make every PreToolUse hook fail open.
func TestCodexAdapter_PreToolUseBlockUsesExitTwo(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("tirith.py", `print(json.dumps({"decision": "block", "reason": "TIRITH_BLOCKED: rm -rf /"}))
sys.exit(1)`)

	got := h.run("PreToolUse", codexBashInput("rm -rf /"), "tirith.py")
	assert.Equal(t, 2, got.exitCode)
	assert.Contains(t, got.stderr, "TIRITH_BLOCKED: rm -rf /")
	assert.Empty(t, got.stdout)
}

// A blocking exit 2 whose stderr is empty is reported as `Failed` by codex,
// which does not block — so the adapter always substitutes a reason.
func TestCodexAdapter_PreToolUseBlockAlwaysCarriesAReason(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("silent.py", "sys.exit(1)")

	got := h.run("PreToolUse", codexBashInput("ls"), "silent.py")
	assert.Equal(t, 2, got.exitCode)
	assert.NotEmpty(t, strings.TrimSpace(got.stderr))
	assert.Contains(t, got.stderr, "silent.py")
}

func TestCodexAdapter_PreToolUseFailsClosedOnUnspawnableScript(t *testing.T) {
	h := newCodexAdapterHarness(t)

	got := h.run("PreToolUse", codexBashInput("ls"), "does-not-exist.py")
	assert.Equal(t, 2, got.exitCode, "a script that cannot run must block, not be skipped")
	assert.Contains(t, got.stderr, "fail closed")
	assert.Contains(t, got.stderr, "does-not-exist.py")
}

func TestCodexAdapter_PreToolUseStopsAtTheFirstBlock(t *testing.T) {
	h := newCodexAdapterHarness(t)
	marker := filepath.Join(h.dir, "second-ran")
	h.script("first.py", `print(json.dumps({"decision": "block", "reason": "first said no"}))
sys.exit(1)`)
	h.script("second.py", `open(`+pyStr(marker)+`, "w").write("x")
sys.exit(0)`)

	got := h.run("PreToolUse", codexBashInput("ls"), "first.py", "second.py")
	assert.Equal(t, 2, got.exitCode)
	assert.Contains(t, got.stderr, "first said no")
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "scripts after a block must not run")
}

// TestCodexAdapter_TranslatesToolNames pins the vocabulary bridge (#608): the
// scripts and FULLSEND_TOOL_ALLOWLIST are written in Claude names, and codex
// reports its own canonical ones.
func TestCodexAdapter_TranslatesToolNames(t *testing.T) {
	h := newCodexAdapterHarness(t)
	seen := filepath.Join(h.dir, "seen.json")
	h.script("record.py", `open(`+pyStr(seen)+`, "w").write(json.dumps(payload))
sys.exit(0)`)

	for codexName, claudeName := range map[string]string{
		"apply_patch":              "Edit",
		"spawn_agent":              "Agent",
		"Bash":                     "Bash",
		"mcp__github__list_issues": "mcp__github__list_issues",
	} {
		input := codexBashInput("touch x")
		input["tool_name"] = codexName
		got := h.run("PreToolUse", input, "record.py")
		require.Equal(t, 0, got.exitCode, got.stderr)

		data, err := os.ReadFile(seen)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(data, &payload))
		assert.Equal(t, claudeName, payload["tool_name"], "codex %q must reach the scripts as %q", codexName, claudeName)
		// tool_input passes through untouched: for Bash and apply_patch it is
		// {"command": "<string>"}, which is what tirith and ssrf read.
		assert.Equal(t, map[string]any{"command": "touch x"}, payload["tool_input"])
	}
}

// TestCodexAdapter_PostToolUseDropsRewriteAndWarns is the other load-bearing
// translation. codex's PostToolUse hookSpecificOutput is deny_unknown_fields
// and accepts only additionalContext and updatedMCPToolOutput, so forwarding
// the sanitizers' updatedToolOutput would make the hook Failed. The rewrite is
// dropped and the model is told the output is untrusted instead.
func TestCodexAdapter_PostToolUseDropsRewriteAndWarns(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("chain.py", `print(json.dumps({
    "tool_result": "token=xxxx",
    "hookSpecificOutput": {
        "hookEventName": "PostToolUse",
        "updatedToolOutput": "token=xxxx",
        "additionalContext": "fullsend: 1 credential-like value(s) were masked",
    },
}))
sys.exit(0)`)

	input := codexBashInput("cat .env")
	input["hook_event_name"] = "PostToolUse"
	input["tool_response"] = "token=sk-live-abcdef"
	got := h.run("PostToolUse", input, "chain.py")
	require.Equal(t, 0, got.exitCode, got.stderr)

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.stdout), &out))
	assert.Equal(t, []string{"hookSpecificOutput"}, sortedKeys(out),
		"only hookSpecificOutput may be emitted; an unknown top-level key makes codex reject the whole object")

	specific := out["hookSpecificOutput"].(map[string]any)
	assert.Equal(t, "PostToolUse", specific["hookEventName"])
	assert.NotContains(t, specific, "updatedToolOutput", "codex cannot rewrite built-in tool output")
	assert.NotContains(t, specific, "updatedMCPToolOutput")
	context := specific["additionalContext"].(string)
	assert.Contains(t, context, "sanitizer would have redacted")
	assert.Contains(t, context, "credential-like value(s) were masked", "the stage's own note is forwarded")
	assert.NotContains(t, got.stdout, "sk-live-abcdef", "the flagged value must not be echoed back")
}

func TestCodexAdapter_PostToolUseUnchangedIsSilent(t *testing.T) {
	h := newCodexAdapterHarness(t)
	// The chain emits only metadata when nothing changed.
	h.script("chain.py", `print(json.dumps({"metadata": {"unicode_findings": 0}}))
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "a.txt\n"
	got := h.run("PostToolUse", input, "chain.py")
	assert.Equal(t, 0, got.exitCode)
	assert.Empty(t, got.stdout)
}

// TestCodexAdapter_PostToolUseCanaryBlocks covers the canary path exactly as
// posttool_chain.py emits it: exit 1, decision block, and `continue: false`.
// On codex `continue: false` neither blocks nor halts, so the adapter must turn
// the whole thing into an exit 2 — which does block, and which withholds the
// original tool output from the model entirely.
func TestCodexAdapter_PostToolUseCanaryBlocks(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("chain.py", `print(json.dumps({
    "decision": "block",
    "reason": "CANARY_LEAKED: canary token found in Bash result",
    "continue": False,
    "tool_result": "[CANARY_REDACTED]",
}))
sys.exit(1)`)

	input := codexBashInput("cat /sandbox/canary")
	input["tool_response"] = "the-canary-value"
	got := h.run("PostToolUse", input, "chain.py")
	assert.Equal(t, 2, got.exitCode)
	assert.Contains(t, got.stderr, "CANARY_LEAKED")
	assert.Empty(t, got.stdout)
	assert.NotContains(t, got.stderr, "the-canary-value")
}

func TestCodexAdapter_PostToolUseChainsInOrder(t *testing.T) {
	h := newCodexAdapterHarness(t)
	h.script("first.py", `print(json.dumps({"hookSpecificOutput": {
    "hookEventName": "PostToolUse", "updatedToolOutput": payload["tool_response"] + "|first"}}))
sys.exit(0)`)
	seen := filepath.Join(h.dir, "second-saw.txt")
	h.script("second.py", `open(`+pyStr(seen)+`, "w").write(payload["tool_response"])
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "base"
	got := h.run("PostToolUse", input, "first.py", "second.py")
	require.Equal(t, 0, got.exitCode, got.stderr)

	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	assert.Equal(t, "base|first", string(data),
		"each stage must see the previous stage's output, as the sanitizer order depends on it")
}

// The scripts read tool_response (contract v2) and fall back to tool_result
// (v1); the adapter sends both so either generation works.
func TestCodexAdapter_PostToolUseSendsBothResultKeys(t *testing.T) {
	h := newCodexAdapterHarness(t)
	seen := filepath.Join(h.dir, "seen.json")
	h.script("record.py", `open(`+pyStr(seen)+`, "w").write(json.dumps(payload))
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "output text"
	got := h.run("PostToolUse", input, "record.py")
	require.Equal(t, 0, got.exitCode, got.stderr)

	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	assert.Equal(t, "output text", payload["tool_response"])
	assert.Equal(t, "output text", payload["tool_result"])
}

func TestCodexAdapter_MisconfigurationFailsClosed(t *testing.T) {
	h := newCodexAdapterHarness(t)

	t.Run("no scripts", func(t *testing.T) {
		cmd := exec.Command(h.python, h.adapter, "PreToolUse")
		cmd.Stdin = strings.NewReader("{}")
		out, err := cmd.CombinedOutput()
		require.Error(t, err)
		assert.Equal(t, 2, exitCodeOf(t, err))
		assert.Contains(t, string(out), "at least one script")
	})

	t.Run("unknown phase", func(t *testing.T) {
		got := h.run("PostToolUseFailure", codexBashInput("ls"), "x.py")
		assert.Equal(t, 2, got.exitCode)
		assert.Contains(t, got.stderr, "unknown codex hook phase")
	})

	// Only empty stdin is benign; a payload that arrived but cannot be read
	// blocks on both phases, since passing it would let a tool call through
	// unscanned.
	for _, phase := range []string{"PreToolUse", "PostToolUse"} {
		t.Run("unreadable payload blocks on "+phase, func(t *testing.T) {
			cmd := exec.Command(h.python, h.adapter, phase, "x.py")
			cmd.Stdin = strings.NewReader("not json")
			out, err := cmd.CombinedOutput()
			require.Error(t, err)
			assert.Equal(t, 2, exitCodeOf(t, err))
			assert.Contains(t, string(out), "fail closed")
		})

		t.Run("a JSON array is not an object either on "+phase, func(t *testing.T) {
			cmd := exec.Command(h.python, h.adapter, phase, "x.py")
			cmd.Stdin = strings.NewReader(`["tool_name","Bash"]`)
			_, err := cmd.CombinedOutput()
			require.Error(t, err)
			assert.Equal(t, 2, exitCodeOf(t, err))
		})
	}

	t.Run("empty stdin is not a tool call", func(t *testing.T) {
		cmd := exec.Command(h.python, h.adapter, "PreToolUse", "x.py")
		cmd.Stdin = strings.NewReader("   ")
		require.NoError(t, cmd.Run(), "every script treats empty stdin as no tool call and allows it")
	})
}

// TestCodexAdapterPhasesMatchHookPlan keeps the phase strings the adapter
// dispatches on equal to the ones codexHooksJSON writes into the command line.
func TestCodexAdapterPhasesMatchHookPlan(t *testing.T) {
	t.Parallel()

	src := string(codexHookAdapterPy)
	assert.Contains(t, src, `PHASE_PRE = "`+string(security.HookPhasePreToolUse)+`"`)
	assert.Contains(t, src, `PHASE_POST = "`+string(security.HookPhasePostToolUse)+`"`)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// pyStr renders a Go string as a Python string literal for the fake scripts.
func pyStr(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// TestCodexAdapter_CrashBlocks covers the last fail-open path: an unexpected
// exception would exit 1, and codex records any exit other than 0 and 2 as
// Failed — which does not block. The top-level handler routes it to a block.
func TestCodexAdapter_CrashBlocks(t *testing.T) {
	h := newCodexAdapterHarness(t)
	// A script whose stdout is valid JSON of the wrong shape: the adapter
	// reaches into it and must not fall over silently.
	h.script("weird.py", `print(json.dumps({"hookSpecificOutput": "not-an-object"}))
sys.exit(0)`)

	input := codexBashInput("ls")
	input["tool_response"] = "out"
	got := h.run("PostToolUse", input, "weird.py")
	assert.Contains(t, []int{0, 2}, got.exitCode,
		"whatever the adapter decides, it must never exit a code codex reads as Failed")

	// And a genuine crash: the hooks directory replaced by a file makes the
	// script lookup raise rather than return.
	require.NoError(t, os.RemoveAll(h.hooksDir))
	require.NoError(t, os.WriteFile(h.hooksDir, []byte("not a directory"), 0o644))
	crashed := h.run("PreToolUse", codexBashInput("ls"), "tirith_check.py")
	assert.Equal(t, 2, crashed.exitCode, "a crash must block, not fail open")
	assert.NotEmpty(t, strings.TrimSpace(crashed.stderr))
}
