package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A credential-shaped value the shared pattern list masks. Split so this test
// file does not itself read as a leaked key to a scanner.
const codexTestSecret = "ghp_" + "0123456789abcdefghijklmnopqrstuvwxyzAB"

func TestCodexRedactJSONLine_MasksToolOutput(t *testing.T) {
	t.Parallel()

	line := []byte(`{"type":"item.completed","item":{"id":"item_4","type":"command_execution",` +
		`"command":"cat .env","aggregated_output":"GITHUB_TOKEN=` + codexTestSecret + `\n",` +
		`"exit_code":0,"status":"completed"}}`)

	got := codexRedactJSONLine(line)
	assert.NotContains(t, string(got), codexTestSecret,
		"the artifact must not carry a credential the hook chain masked for the model")

	var out map[string]any
	require.NoError(t, json.Unmarshal(got, &out))
	item := out["item"].(map[string]any)
	// Structure and the non-secret fields survive: the artifact is still a
	// parseable stream capture, which ParseTranscriptFile depends on.
	assert.Equal(t, "command_execution", item["type"])
	assert.Equal(t, "cat .env", item["command"])
	assert.Equal(t, "completed", item["status"])
	assert.Contains(t, item["aggregated_output"], "GITHUB_TOKEN=")
	// Numbers keep their literal form: without UseNumber a round trip through
	// float64 could re-emit exit_code or a token count in exponent form.
	assert.Contains(t, string(got), `"exit_code":0`)
}

func TestCodexRedactJSONLine_PassesCleanLinesThrough(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		`{"type":"thread.started","thread_id":"01a06320-1e52-7900-8fd9-dfe2f2a0cd4c"}`,
		`{"type":"turn.completed","usage":{"input_tokens":1200,"output_tokens":48}}`,
	} {
		var before, after any
		require.NoError(t, json.Unmarshal([]byte(line), &before))
		require.NoError(t, json.Unmarshal(codexRedactJSONLine([]byte(line)), &after))
		assert.Equal(t, before, after, "a line with nothing to redact must survive intact")
	}
	assert.Equal(t, "", string(codexRedactJSONLine([]byte(""))))
}

// A line that is not JSON is where a truncated or hostile payload would sit,
// so it is redacted as text rather than passed through untouched.
func TestCodexRedactJSONLine_RedactsNonJSON(t *testing.T) {
	t.Parallel()

	got := string(codexRedactJSONLine([]byte(`{"type":"item.completed","truncated ` + codexTestSecret)))
	assert.NotContains(t, got, codexTestSecret)
}

func TestCodexRedactingWriter_HandlesSplitWrites(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	w := newCodexRedactingWriter(&sink)

	full := `{"type":"item.completed","item":{"type":"agent_message","text":"key ` +
		codexTestSecret + `"}}` + "\n" + `{"type":"turn.completed"}` + "\n"
	// Write it in awkward chunks: the tee gives whatever the reader produced,
	// which does not align with lines.
	for i := 0; i < len(full); i += 7 {
		end := min(i+7, len(full))
		n, err := w.Write([]byte(full[i:end]))
		require.NoError(t, err)
		assert.Equal(t, end-i, n, "the tee side must always report a full write")
	}
	require.NoError(t, w.Flush())

	out := sink.String()
	assert.NotContains(t, out, codexTestSecret)
	assert.Equal(t, 2, strings.Count(out, "\n"), "line framing must be preserved")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var v any
		assert.NoError(t, json.Unmarshal([]byte(line), &v), "each line stays valid JSON: %s", line)
	}
}

func TestCodexRedactingWriter_FlushesAPartialLine(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	w := newCodexRedactingWriter(&sink)
	// A stream cut mid-line still has to leave a redacted tail behind.
	_, err := w.Write([]byte(`{"type":"item.completed","item":{"text":"` + codexTestSecret))
	require.NoError(t, err)
	assert.Empty(t, sink.String(), "nothing is written until the line is complete")

	require.NoError(t, w.Flush())
	assert.NotEmpty(t, sink.String())
	assert.NotContains(t, sink.String(), codexTestSecret)
	assert.NoError(t, w.Flush(), "flushing an empty buffer is a no-op")
}

func TestCodexRedactFile_RewritesRolloutInPlace(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rollout-2026-09-02T10-00-00-abc.jsonl")
	body := `{"type":"session_meta","payload":{"id":"abc"}}` + "\n" +
		`{"type":"response_item","payload":{"output":"token ` + codexTestSecret + `"}}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	require.NoError(t, codexRedactFile(path))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(got), codexTestSecret)
	assert.Contains(t, string(got), "session_meta", "the rollout stays readable")
	assert.Equal(t, 2, strings.Count(string(got), "\n"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the file's mode is preserved")
}

func TestCodexRedactFile_RefusesCompressedRollouts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "rollout-old.jsonl.zst")
	require.NoError(t, os.WriteFile(path, []byte("not really zstd"), 0o644))

	err := codexRedactFile(path)
	require.Error(t, err, "a compressed rollout must be reported, not silently left unredacted")
	assert.Contains(t, err.Error(), "compressed")
}

// TestCodexRun_TeedOutputIsRedacted is the end-to-end half: the parser sees the
// original stream and the artifact on disk does not carry the secret.
func TestCodexRun_TeedOutputIsRedacted(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	r := CodexRuntime{}
	seedCodexManifest(t, storeDir, r, nil)

	fixture := filepath.Join(t.TempDir(), "leaky.jsonl")
	require.NoError(t, os.WriteFile(fixture, []byte(
		`{"type":"thread.started","thread_id":"t1"}`+"\n"+
			`{"type":"turn.started"}`+"\n"+
			`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"cat .env",`+
			`"aggregated_output":"GITHUB_TOKEN=`+codexTestSecret+`","exit_code":0,"status":"completed"}}`+"\n"+
			`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`+"\n"), 0o644))
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.152.1", fixture)

	outPath := filepath.Join(t.TempDir(), "output.jsonl")
	metrics := &RunMetrics{}
	exit, err := r.Run(t.Context(), RunParams{
		SandboxName: "sb",
		RepoDir:     "/sandbox/workspace/repo",
		Model:       "gpt-5-mini",
		OutputPath:  outPath,
		Timeout:     time.Minute,
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	// The parser still counted the tool call, so redaction happens on the tee
	// branch only and does not change what the run reports.
	assert.EqualValues(t, 1, metrics.ToolCalls.Load())

	got, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.NotContains(t, string(got), codexTestSecret)
	assert.Contains(t, string(got), "command_execution")

	// And the artifact is still a stream capture the verdict helper accepts.
	te, ok := CodexRuntime{}.ParseTranscriptFile(outPath)
	require.True(t, ok, "the redacted artifact must still parse as a codex stream")
	assert.False(t, te.IsError)
}
