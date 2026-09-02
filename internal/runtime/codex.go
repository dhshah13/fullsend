package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// codexDebugLogFile is the per-iteration debug artifact for codex runs
// (codex writes its tracing output to stderr, which Run will tee here).
const codexDebugLogFile = "codex-debug.log"

// codexNotImplemented is the shared not-implemented message. Every stub
// method names the tracking issue so a CI log never shows an unattributed
// failure.
const codexNotImplemented = "codex runtime is not yet implemented (#6920)"

// CodexRuntime is a stub implementation of the Runtime and TranscriptHandler
// interfaces for the Codex agent runtime (openai/codex, CLI `codex`, pinned
// in the sandbox image by CODEX_VERSION). All methods are no-ops or return
// not-implemented errors; subsequent PRs fill in stream parsing, bootstrap,
// run execution, and transcript extraction (#6920). It is registered in
// Resolve() but deliberately absent from config.ValidRuntimes(), so no
// per-repo config (nor an agents: entry) can select it until it works.
type CodexRuntime struct{}

func (CodexRuntime) Name() string { return "codex" }

// System returns the OTEL GenAI gen_ai.system value. Unlike pi and opencode,
// codex serves a single model vendor — it speaks the OpenAI Responses API and
// has no Vertex, Anthropic or Gemini path — so the system is the vendor
// ("openai"), not the runtime name.
func (CodexRuntime) System() string { return "openai" }

// ConfigDir returns the codex config directory inside the sandbox. It is
// exported to the agent process as CODEX_HOME (see EnvExports) and lives
// outside the cloned repo tree so the target repo cannot pre-seed it and
// workspace resets do not clear it. It is not a permission boundary: the
// agent process runs as the same user, so the runner-written files under it
// must be checksum-guarded before each launch rather than trusted.
func (CodexRuntime) ConfigDir() string { return sandbox.SandboxCodexConfig }

func (CodexRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

// EnvExports pins codex's config location to the runner-owned path. codex
// refuses to start when CODEX_HOME does not exist, so Bootstrap must create
// the directory as well; the sandbox image bakes the same value as an ENV
// default for ad-hoc invocations (images/sandbox/Containerfile). codex keeps
// its other hygiene settings (update check, analytics, telemetry) in
// config.toml rather than the environment, so there is nothing else here.
func (r CodexRuntime) EnvExports() []string {
	return []string{fmt.Sprintf("export CODEX_HOME=%s", r.ConfigDir())}
}

func (CodexRuntime) Bootstrap(_ BootstrapInput) error {
	return errors.New(codexNotImplemented)
}

func (CodexRuntime) Run(_ context.Context, _ RunParams, _ *ui.Printer, _ time.Time, _ *RunMetrics) (int, error) {
	return -1, errors.New(codexNotImplemented)
}

// ClearIterationArtifacts is a no-op while Run is a stub: nothing has run in
// the sandbox, so there is nothing to clear. When Run is implemented this
// must sweep stray sandbox processes (clearStrayProcesses, see
// killStrayProcesses) before removing the iteration's files, like the other
// runtimes — the Runtime interface documents that as part of the contract.
func (CodexRuntime) ClearIterationArtifacts(_ string) error { return nil }

// DebugLogName implements DebugLogNamer: the local artifact for codex's
// stderr trace output.
func (CodexRuntime) DebugLogName() string { return codexDebugLogFile }

// OpenAIAuthSeed and OpenAIAuthFile implement OpenAICredentialSeeder. They
// are stubs until Run exists (#6920): codex reads its bearer token by
// running an auth command that prints the placeholder from a runner-owned
// token file under CODEX_HOME, and both the file and the fragment that
// seeds it are written by Bootstrap. An empty seed is how the runner tells
// a backend has no re-seed to perform, so until then a codex run's
// run-scoped provider is created and refreshed but nothing is re-seeded
// inside the sandbox.

func (CodexRuntime) OpenAIAuthSeed() string { return "" }

func (CodexRuntime) OpenAIAuthFile() string { return "" }

// TranscriptHandler stub methods — return not-implemented errors for extract
// methods (to avoid silent success claims in CI logs) and no-ops for parse
// methods (which correctly indicate "nothing found"). See #6920.

func (CodexRuntime) ExtractTranscripts(_, _, _ string) error {
	return errors.New("codex transcript extraction not implemented (#6920)")
}

func (CodexRuntime) ExtractDebugLog(_, _, _ string) error {
	return errors.New("codex debug log extraction not implemented (#6920)")
}

func (CodexRuntime) ParseTranscriptErrors(_ string) []TranscriptError { return nil }

func (CodexRuntime) ParseTranscriptFile(_ string) (TranscriptError, bool) {
	return TranscriptError{}, false
}

func (CodexRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

// Compile-time interface assertions.
var (
	_ Runtime           = CodexRuntime{}
	_ TranscriptHandler = CodexRuntime{}
	_ DebugLogNamer     = CodexRuntime{}

	_ OpenAICredentialSeeder = CodexRuntime{}
)
