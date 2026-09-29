package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexRolloutLargeCommandOutput(t *testing.T) {
	// Native item_completed records retain stdout and aggregated_output. A
	// real 0.157.0 command produced a 1,455,304-byte JSONL record this way,
	// exceeding the other runtimes' 1 MiB line limit within a small artifact.
	output := strings.Repeat("harmless command output\n", 32000) + codexTestSecret
	event, err := json.Marshal(map[string]any{
		"type": "event_msg",
		"payload": map[string]any{
			"type": "item_completed", "thread_id": "child", "turn_id": "turn",
			"item": map[string]any{
				"type": "CommandExecution", "status": "completed",
				"stdout": output, "aggregated_output": output, "stderr": "",
			},
		},
	})
	require.NoError(t, err)
	data := strings.Join([]string{
		codexUsageTestMeta,
		codexUsageTestContext("turn", "gpt-5.6-luna"),
		string(event),
		codexUsageTestRecord(t, "after-large-output", "turn", codexUsage{InputTokens: 123, OutputTokens: 45}),
	}, "\n") // Exercise a final record without a trailing newline too.

	t.Run("usage after large output is retained", func(t *testing.T) {
		got, err := parseCodexRolloutUsage(strings.NewReader(data))
		require.NoError(t, err)
		require.Len(t, got.Responses, 1)
		assert.Equal(t, "after-large-output", got.Responses[0].Record.ResponseID)
		assert.Equal(t, "gpt-5.6-luna", got.Responses[0].Model)
		assert.Equal(t, 123, got.Responses[0].Record.Usage.InputTokens)
		assert.Equal(t, 45, got.Responses[0].Record.Usage.OutputTokens)
	})

	t.Run("complete artifact is validated and redacted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rollout.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		require.NoError(t, codexIsRolloutFile(path))
		require.NoError(t, codexRedactFile(path))
		redacted, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NotContains(t, string(redacted), codexTestSecret)
		lines := bytes.Split(redacted, []byte{'\n'})
		require.Len(t, lines, 4, "large records must not be split or discarded")
		for _, line := range lines {
			require.True(t, json.Valid(line))
		}
		var item struct {
			Payload struct {
				Item struct {
					Stdout string `json:"stdout"`
				} `json:"item"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(lines[2], &item))
		assert.Equal(t, 32000, strings.Count(item.Payload.Item.Stdout, "harmless command output\n"))
		assert.Contains(t, string(lines[3]), "after-large-output")
		require.NoError(t, codexIsRolloutFile(path))
	})

	t.Run("invalid evidence after large output is rejected", func(t *testing.T) {
		for _, tail := range []string{"not JSON", `{"type":"not_a_codex_envelope","payload":{}}`} {
			bad := data + "\n" + tail
			_, err := parseCodexRolloutUsage(strings.NewReader(bad))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 5", "must validate past the large record")
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(bad), 0o600))
			err = codexIsRolloutFile(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "line 5")
		}
	})
}

func TestCodexRolloutScannerPreviousLineBoundary(t *testing.T) {
	// An exact-size buffer needs room to discover EOF or consume a newline.
	// Both paths must accept records around the previous 1 MiB boundary.
	const prefix = `{"type":"response_item","payload":{"type":"function_call_output","output":"`
	const suffix = `"}}`
	for _, size := range []int{1<<20 - 1, 1 << 20, 1<<20 + 1} {
		for _, newline := range []bool{false, true} {
			t.Run(fmt.Sprintf("bytes=%d/newline=%t", size, newline), func(t *testing.T) {
				line := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
				data := codexUsageTestMeta + "\n" + line
				if newline {
					data += "\n"
				}
				_, err := parseCodexRolloutUsage(strings.NewReader(data))
				require.NoError(t, err)
				path := filepath.Join(t.TempDir(), "rollout.jsonl")
				require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
				require.NoError(t, codexIsRolloutFile(path))
			})
		}
	}
}

// Generate an unending stream without allocating an artifact-sized fixture.
// Blank records force the parser to reach its byte bound instead of stopping
// earlier at an invalid JSON record.
type codexBlankRolloutReader struct{ bytesRead int }

func (r *codexBlankRolloutReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	if len(p) > 0 {
		p[len(p)-1] = '\n'
	}
	r.bytesRead += len(p)
	return len(p), nil
}

func TestCodexRolloutUsageRejectsArtifactPastByteLimit(t *testing.T) {
	r := &codexBlankRolloutReader{}
	_, err := parseCodexRolloutUsage(r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact limit")
	assert.LessOrEqual(t, r.bytesRead, 256<<20+1, "read no further than the bound plus its overflow probe")
}
