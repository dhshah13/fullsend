package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// OpenCodeRuntime is a stub implementation of the Runtime and TranscriptHandler
// interfaces for the OpenCode agent runtime. All methods are no-ops or return
// not-implemented errors. Subsequent PRs will fill in stream parsing, bootstrap,
// run execution, and transcript extraction.
type OpenCodeRuntime struct{}

func (OpenCodeRuntime) Name() string { return "opencode" }

// System returns the OTEL GenAI gen_ai.system value. OpenCode is multi-provider
// (Anthropic, OpenAI, Google, etc.), so the system is the runtime itself rather
// than a single model vendor. The actual model vendor may be capturable from
// opencode's stream/export events in a future PR once the event schema is
// confirmed (see #1935).
func (OpenCodeRuntime) System() string { return "opencode" }

// ConfigDir returns the opencode config directory inside the sandbox.
// Provisional — verify opencode's config discovery before implementing
// Bootstrap (#1260). Consider placing config outside the agent-writable
// workspace (like Claude's /sandbox/claude-config) to prevent the agent
// from rewriting its own runtime config.
func (OpenCodeRuntime) ConfigDir() string { return sandbox.SandboxWorkspace + "/.opencode" }

func (OpenCodeRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

func (OpenCodeRuntime) EnvExports() []string { return nil }

func (OpenCodeRuntime) Bootstrap(_ BootstrapInput) error {
	return fmt.Errorf("opencode runtime is not yet implemented")
}

func (OpenCodeRuntime) Run(_ context.Context, _ RunParams, _ *ui.Printer, _ time.Time, _ *RunMetrics) (int, error) {
	return -1, fmt.Errorf("opencode runtime is not yet implemented")
}

// ClearIterationArtifacts is a no-op while Run is a stub: nothing has run in
// the sandbox, so there is nothing to clear. When Run is implemented this
// must sweep stray sandbox processes (clearStrayProcesses, see
// killStrayProcesses) before removing the iteration's files, like the other
// runtimes — the Runtime interface documents that as part of the contract.
func (OpenCodeRuntime) ClearIterationArtifacts(_ string) error { return nil }

// TranscriptHandler stub methods — return not-implemented errors for extract
// methods (to avoid silent success claims in CI logs) and no-ops for parse
// methods (which correctly indicate "nothing found"). See #1935.

func (OpenCodeRuntime) ExtractTranscripts(_, _, _ string) error {
	return fmt.Errorf("opencode transcript extraction not implemented (see #1935)")
}

func (OpenCodeRuntime) ExtractDebugLog(_, _, _ string) error {
	return fmt.Errorf("opencode debug log extraction not implemented (see #1935)")
}

func (OpenCodeRuntime) ParseTranscriptErrors(_ string) []TranscriptError { return nil }

func (OpenCodeRuntime) ParseTranscriptFile(_ string) (TranscriptError, bool) {
	return TranscriptError{}, false
}

func (OpenCodeRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

// Compile-time interface assertions.
var (
	_ Runtime           = OpenCodeRuntime{}
	_ TranscriptHandler = OpenCodeRuntime{}
)
