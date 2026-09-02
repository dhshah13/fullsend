package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestCodexExtractTranscripts_DownloadsRollouts(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	rollout := r.codexSessionsDir() + "/2026/09/02/rollout-2026-09-02T10-00-00-abc123.jsonl"
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1", "", rollout)

	outDir := filepath.Join(t.TempDir(), "transcripts")
	require.NoError(t, r.ExtractTranscripts("sb", "triage", outDir))

	log := readFileString(t, logPath)
	// Both suffixes are collected: codex compresses older rollouts in place.
	assert.Contains(t, log, "*.jsonl")
	assert.Contains(t, log, "*.jsonl.zst")
	assert.Contains(t, log, "download")
	// The local name is prefixed with the agent label, as for pi and Claude,
	// so several agents' transcripts can share one directory.
	_, err := os.Stat(filepath.Join(outDir, "triage-rollout-2026-09-02T10-00-00-abc123.jsonl"))
	assert.NoError(t, err)
}

func TestCodexExtractTranscripts_NoSessionsIsNotAnError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "0.152.1")

	outDir := filepath.Join(t.TempDir(), "transcripts")
	require.NoError(t, CodexRuntime{}.ExtractTranscripts("sb", "triage", outDir))
	assert.NotContains(t, readFileString(t, logPath), "download")
}

func TestCodexExtractDebugLog_OnlyWhenDebugIsOn(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "0.152.1")
	local := filepath.Join(t.TempDir(), "codex-debug.log")

	require.NoError(t, CodexRuntime{}.ExtractDebugLog("sb", local, ""))
	assert.NotContains(t, readFileString(t, logPath), codexDebugLogFile)

	require.NoError(t, CodexRuntime{}.ExtractDebugLog("sb", local, "1"))
	assert.Contains(t, readFileString(t, logPath), codexDebugLogFile)
}

func TestCodexParseTranscriptErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	copyFixture := func(name, dest string) {
		data, err := os.ReadFile(filepath.Join("testdata", "codex", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, dest), data, 0o644))
	}
	copyFixture("turn_failed.jsonl", "output.jsonl")
	copyFixture("basic_run.jsonl", "ok.jsonl")
	// A rollout session file is a different envelope; it must be skipped
	// rather than misread as a failed run.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout.jsonl"),
		[]byte(`{"type":"session_meta","payload":{"id":"abc"}}`+"\n"), 0o644))
	// Non-JSONL files are ignored entirely.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "last-message.txt"), []byte("done"), 0o644))

	got := CodexRuntime{}.ParseTranscriptErrors(dir)
	require.Len(t, got, 1)
	assert.Equal(t, "output.jsonl", got[0].Source)
	assert.True(t, got[0].IsError)
}

func TestCodexParseTranscriptErrors_MissingDir(t *testing.T) {
	t.Parallel()

	assert.Nil(t, CodexRuntime{}.ParseTranscriptErrors(filepath.Join(t.TempDir(), "nope")))
}

func TestCodexParseTranscriptFile(t *testing.T) {
	t.Parallel()

	te, ok := CodexRuntime{}.ParseTranscriptFile(filepath.Join("testdata", "codex", "turn_failed.jsonl"))
	require.True(t, ok)
	assert.True(t, te.IsError)

	_, ok = CodexRuntime{}.ParseTranscriptFile(filepath.Join(t.TempDir(), "absent.jsonl"))
	assert.False(t, ok)
}

// TestCodexRun_StreamVerdictOverridesExitZero is the runtime half of the
// exit-code contract: `codex exec` exits 0 on a failed turn, so a run whose
// stream reports a failure must still be reported as failed.
func TestCodexRun_StreamVerdictOverridesExitZero(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1", filepath.Join("testdata", "codex", "turn_failed.jsonl"))

	outPath := filepath.Join(t.TempDir(), "output.jsonl")
	metrics := &RunMetrics{}
	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "openai/gpt-5.6-luna",
		Effort:      "high",
		OutputPath:  outPath,
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)

	require.NoError(t, err)
	assert.Equal(t, 1, exit, "a stream-reported failure must override the zero exit")
	// The stream carries no model, so the runner supplies the resolved id.
	assert.Equal(t, "gpt-5.6-luna", metrics.Model)

	// The stream is tee'd so ParseTranscriptFile can reach the same verdict.
	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestCodexRun_SuccessfulRunReportsMetrics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1", filepath.Join("testdata", "codex", "basic_run.jsonl"))

	metrics := &RunMetrics{}
	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "gpt-5.6-luna",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Positive(t, metrics.InputTokens)
	assert.Positive(t, metrics.OutputTokens)
	// codex reports no cost, so it stays zero rather than being guessed at.
	assert.Zero(t, metrics.TotalCostUSD)
}

func TestCodexRun_RejectsForeignModelBeforeSpending(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1")

	exit, err := r.Run(context.Background(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "anthropic-vertex/claude-opus-4-6",
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.Error(t, err)
	assert.Equal(t, -1, exit)
	assert.Contains(t, err.Error(), "codex serves OpenAI models only")
	assert.NotContains(t, readFileString(t, logPath), "exec --json", "the run must not start")
}

// Security is a runner-side decision; a manifest without a hook plan while the
// runner says hooks are on means the wiring was lost, and running anyway would
// be the silently-unhooked failure ADR 0090 forbids.
func TestCodexRun_RefusesHooklessRunWhenSecurityIsOn(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1")

	_, err := r.Run(context.Background(), RunParams{
		SandboxName:       "sb",
		RepoDir:           "/sandbox/workspace/repo",
		Model:             "gpt-5.6-luna",
		HooksSettingsPath: r.codexHooksPath(),
		Timeout:           time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "carries no hook plan")
	assert.NotContains(t, readFileString(t, logPath), "exec --json")
}

func TestCodexRun_AcceptsManifestHookPlan(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r,
		codexHooksManifestFor(r.codexHooksDir(), security.SandboxHookConfigFromHarness(&harness.Harness{})))
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1", filepath.Join("testdata", "codex", "basic_run.jsonl"))

	exit, err := r.Run(context.Background(), RunParams{
		SandboxName:       "sb",
		RepoDir:           "/sandbox/workspace/repo",
		Model:             "gpt-5.6-luna",
		HooksSettingsPath: r.codexHooksPath(),
		Timeout:           time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Contains(t, readFileString(t, logPath), "--dangerously-bypass-hook-trust")
}

// seedCodexManifest puts a manifest into the fake openshell's store so
// readCodexManifest finds one.
func seedCodexManifest(t *testing.T, storeDir string, r CodexRuntime, hooks *codexHooksManifest) {
	t.Helper()
	data, err := json.MarshalIndent(codexManifest{
		AgentName:    "triage",
		Model:        "openai/gpt-5.6-luna",
		CodexVersion: "codex-cli 0.152.1",
		Hooks:        hooks,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	name := r.codexManifestPath()
	require.NoError(t, os.WriteFile(filepath.Join(storeDir, sanitizeStorePath(name)), data, 0o644))
}

func sanitizeStorePath(p string) string {
	out := make([]rune, 0, len(p))
	for _, r := range p {
		if r == '/' {
			r = '_'
		}
		out = append(out, r)
	}
	return string(out)
}
