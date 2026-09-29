package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readRecordedCodexUsage(t *testing.T, version string) (string, []codexRolloutUsage) {
	t.Helper()
	dir := filepath.Join("testdata", "codex", "native-subagents", version)
	var rootID string
	var rollouts []codexRolloutUsage
	for _, name := range []string{"root.jsonl", "probe-alpha.jsonl", "probe-beta.jsonl", "default.jsonl"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		rec, err := parseCodexRolloutUsage(strings.NewReader(string(data)))
		require.NoError(t, err, name)
		if name == "root.jsonl" {
			rootID = rec.Meta.ThreadID
			require.Len(t, rec.Responses, 6, "capture has six parent API responses")
		} else {
			require.Len(t, rec.Responses, 1, "capture has one response per child")
		}
		rollouts = append(rollouts, rec)
	}
	return rootID, rollouts
}

func TestCodexSubagentUsage_RecordedNativeVersions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version   string
		output    int
		reasoning int
		parent    int
		combined  int
	}{
		{"0157", 24, 0, 96500, 124159},
		{"0158", 26, 11, 96985, 124657},
	} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			rootID, rollouts := readRecordedCodexUsage(t, tc.version)
			byModel, err := foldCodexChildUsage(rootID, rollouts)
			require.NoError(t, err)
			require.Len(t, byModel, 1)
			got := byModel["gpt-5.6-luna"]
			assert.Equal(t, ModelUsage{
				Requests: 3, InputTokens: 9, OutputTokens: tc.output, CostUnavailable: true,
				ReasoningTokens: tc.reasoning, CacheCreationInputTokens: 27626,
			}, got)
			assert.Equal(t, tc.combined, tc.parent+got.InputTokens+got.OutputTokens+
				got.ReasoningTokens+got.CacheReadInputTokens+got.CacheCreationInputTokens,
				"all disjoint child counters must conserve the recorded raw total")
			assert.Zero(t, got.CostUSD, "the captures report no dollar cost")
		})
	}
}

func TestCodexSubagentUsage_DeduplicatesResponsesAndCopiedRollouts(t *testing.T) {
	t.Parallel()
	rootID, rollouts := readRecordedCodexUsage(t, "0158")
	want, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	rollouts[1].Responses = append(rollouts[1].Responses, rollouts[1].Responses[0])
	rollouts = append(rollouts, rollouts[1], rollouts[2], rollouts[3])
	got, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	assert.Equal(t, want, got, "duplicates must not create extra agent invocations")
}

func TestCodexSubagentUsage_IdentityIsThreadNotRoleOrResponseIDAlone(t *testing.T) {
	t.Parallel()
	rootID, rollouts := readRecordedCodexUsage(t, "0158")
	want, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	for i := 1; i < len(rollouts); i++ {
		rollouts[i].Meta.Role = "same-persona"
		rollouts[i].Responses[0].Record.ResponseID = "same-response-id-in-distinct-threads"
	}
	got, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestCodexSubagentUsage_ExcludesRootStaleSessionsAndInheritedResponses(t *testing.T) {
	t.Parallel()
	rootID, rollouts := readRecordedCodexUsage(t, "0158")
	want, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	// A full-history fork can carry the parent's own recorded responses.
	rollouts[1].Responses = append(rollouts[1].Responses, rollouts[0].Responses...)
	_, stale := readRecordedCodexUsage(t, "0157")
	rollouts = append(rollouts, stale...)
	got, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestCodexSubagentUsage_RejectsConflictingDuplicate(t *testing.T) {
	t.Parallel()
	for _, conflict := range []string{"usage", "model", "turn", "session"} {
		t.Run(conflict, func(t *testing.T) {
			t.Parallel()
			rootID, rollouts := readRecordedCodexUsage(t, "0158")
			_, copyRollouts := readRecordedCodexUsage(t, "0158")
			duplicate := copyRollouts[1]
			switch conflict {
			case "usage":
				duplicate.Responses[0].Record.Usage.InputTokens++
			case "model":
				duplicate.Responses[0].Model = "gpt-5.6-sol"
			case "turn":
				duplicate.Responses[0].Record.TurnID = "another-turn"
			case "session":
				duplicate.Responses[0].Record.SessionID = "another-root"
			}
			_, err := foldCodexChildUsage(rootID, append(rollouts, duplicate))
			require.Error(t, err, "conflicting duplicate evidence must not silently win or be added twice")
		})
	}
}

// Synthetic cases below mutate the recorded wire shape. Their values are
// intentionally separate from the recorded native fixture counts above.
const codexUsageTestMeta = `{"type":"session_meta","payload":{"id":"child","session_id":"root","parent_thread_id":"root","agent_role":"test"}}`

func codexUsageTestRecord(t *testing.T, responseID, turnID string, usage any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"type": "token_usage_record",
		"payload": map[string]any{
			"thread_id": "child", "session_id": "root", "turn_id": turnID,
			"root_turn_id": "root-turn", "response_id": responseID, "usage": usage,
		},
	})
	require.NoError(t, err)
	return string(data)
}

func codexUsageTestContext(turnID, model string) string {
	data, _ := json.Marshal(map[string]any{
		"type": "turn_context", "payload": map[string]string{
			"turn_id": turnID, "model": model, "root_turn_id": "root-turn",
		},
	})
	return string(data)
}

func TestCodexSubagentUsage_AttributesEachResponseToItsTurnModel(t *testing.T) {
	t.Parallel()
	u1 := codexUsage{InputTokens: 100, CachedInputTokens: 60, CacheWriteInputTokens: 30, OutputTokens: 20, ReasoningOutputTokens: 5}
	u2 := codexUsage{InputTokens: 200, CachedInputTokens: 100, CacheWriteInputTokens: 80, OutputTokens: 30, ReasoningOutputTokens: 6}
	data := strings.Join([]string{
		codexUsageTestMeta,
		codexUsageTestContext("turn-one", "gpt-5.6-luna"),
		codexUsageTestContext("turn-two", "gpt-5.6-sol"),
		codexUsageTestRecord(t, "response-one", "turn-one", u1),
		codexUsageTestRecord(t, "response-two", "turn-two", u2),
	}, "\n")
	rec, err := parseCodexRolloutUsage(strings.NewReader(data))
	require.NoError(t, err)
	got, err := foldCodexChildUsage("root", []codexRolloutUsage{rec})
	require.NoError(t, err)
	assert.Equal(t, map[string]ModelUsage{
		"gpt-5.6-luna": {Requests: 1, InputTokens: 10, OutputTokens: 15, CostUnavailable: true, ReasoningTokens: 5, CacheReadInputTokens: 60, CacheCreationInputTokens: 30},
		"gpt-5.6-sol":  {Requests: 1, InputTokens: 20, OutputTokens: 24, CostUnavailable: true, ReasoningTokens: 6, CacheReadInputTokens: 100, CacheCreationInputTokens: 80},
	}, got)
}

func TestCodexSubagentUsage_MissingModelIsUnknown(t *testing.T) {
	t.Parallel()
	data := codexUsageTestMeta + "\n" + codexUsageTestRecord(t, "response", "turn", codexUsage{InputTokens: 8, OutputTokens: 3})
	rec, err := parseCodexRolloutUsage(strings.NewReader(data))
	require.NoError(t, err)
	got, err := foldCodexChildUsage("root", []codexRolloutUsage{rec})
	require.NoError(t, err)
	assert.Equal(t, map[string]ModelUsage{"unknown": {Requests: 1, InputTokens: 8, OutputTokens: 3, CostUnavailable: true}}, got)
}

func TestCodexSubagentUsage_DoesNotRequireSuccessfulChildCompletion(t *testing.T) {
	t.Parallel()
	rootID, rollouts := readRecordedCodexUsage(t, "0158")
	// The reduced captures deliberately have no terminal lifecycle event.
	// Valid incurred response usage still counts when a child later fails.
	got, err := foldCodexChildUsage(rootID, rollouts[1:])
	require.NoError(t, err)
	assert.Equal(t, 11, got["gpt-5.6-luna"].ReasoningTokens)
}

func TestCodexSubagentUsage_RejectsMalformedUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		usage any
	}{
		{"null usage", nil},
		{"empty usage", map[string]int{}},
		{"missing output", map[string]int{"input_tokens": 1}},
		{"negative input", codexUsage{InputTokens: -1}},
		{"negative output", codexUsage{OutputTokens: -1}},
		{"negative cache read", codexUsage{InputTokens: 10, CachedInputTokens: -1}},
		{"negative cache write", codexUsage{InputTokens: 10, CacheWriteInputTokens: -1}},
		{"negative reasoning", codexUsage{OutputTokens: 10, ReasoningOutputTokens: -1}},
		{"cache exceeds input", codexUsage{InputTokens: 10, CachedInputTokens: 8, CacheWriteInputTokens: 3}},
		{"reasoning exceeds output", codexUsage{OutputTokens: 10, ReasoningOutputTokens: 11}},
		{"numeric overflow", json.RawMessage(`{"input_tokens":9223372036854775808}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := codexUsageTestMeta + "\n" + codexUsageTestRecord(t, "response", "turn", tc.usage)
			_, err := parseCodexRolloutUsage(strings.NewReader(data))
			require.Error(t, err)
		})
	}
	require.Error(t, validateCodexResponseUsage(nil), "the fold dereferences validated usage")
}

func TestCodexSubagentUsage_RejectsIncompleteResponseIdentityAndJSON(t *testing.T) {
	t.Parallel()
	valid := codexUsageTestMeta + "\n" + codexUsageTestRecord(t, "response", "turn", codexUsage{InputTokens: 1})
	for _, field := range []string{"thread_id", "session_id", "turn_id", "root_turn_id", "response_id"} {
		t.Run(field, func(t *testing.T) {
			parts := strings.Split(valid, "\n")
			var event map[string]any
			require.NoError(t, json.Unmarshal([]byte(parts[1]), &event))
			delete(event["payload"].(map[string]any), field)
			data, err := json.Marshal(event)
			require.NoError(t, err)
			_, err = parseCodexRolloutUsage(strings.NewReader(parts[0] + "\n" + string(data)))
			require.Error(t, err)
		})
	}
	_, err := parseCodexRolloutUsage(strings.NewReader(valid + "\n{\"type\":"))
	require.Error(t, err, "a truncated tail is not a complete rollout")
}

func TestCodexSubagentUsage_RejectsAccumulationOverflow(t *testing.T) {
	t.Parallel()
	maxInt := int(^uint(0) >> 1)
	data := strings.Join([]string{
		codexUsageTestMeta,
		codexUsageTestRecord(t, "one", "turn", codexUsage{InputTokens: maxInt}),
		codexUsageTestRecord(t, "two", "turn", codexUsage{InputTokens: 1}),
	}, "\n")
	rec, err := parseCodexRolloutUsage(strings.NewReader(data))
	require.NoError(t, err)
	_, err = foldCodexChildUsage("root", []codexRolloutUsage{rec})
	require.Error(t, err, "untrusted counters must not wrap negative")
}

func TestCodexSubagentUsage_AddPreservesParentAndReasoning(t *testing.T) {
	t.Parallel()
	rootID, rollouts := readRecordedCodexUsage(t, "0158")
	byModel, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	parent := ModelUsage{Requests: 1, InputTokens: 18, OutputTokens: 624, CostUnavailable: true,
		ReasoningTokens: 170, CacheReadInputTokens: 78328, CacheCreationInputTokens: 17845}
	m := &RunMetrics{InputTokens: parent.InputTokens, OutputTokens: parent.OutputTokens,
		ReasoningTokens: parent.ReasoningTokens, CacheReadInputTokens: parent.CacheReadInputTokens,
		CacheCreationInputTokens: parent.CacheCreationInputTokens,
		PerModelUsage:            map[string]ModelUsage{"gpt-5.6-luna": parent}}
	require.NoError(t, addCodexUsage(m, byModel))
	assert.Equal(t, 27, m.InputTokens)
	assert.Equal(t, 650, m.OutputTokens)
	assert.Equal(t, 181, m.ReasoningTokens)
	assert.Equal(t, 45471, m.CacheCreationInputTokens)
	assert.Equal(t, 78328, m.CacheReadInputTokens)
	assert.Equal(t, ModelUsage{Requests: 4, InputTokens: 27, OutputTokens: 650, CostUnavailable: true,
		ReasoningTokens: 181, CacheReadInputTokens: 78328, CacheCreationInputTokens: 45471},
		m.PerModelUsage["gpt-5.6-luna"])

	var summed ModelUsage
	summed.Add(parent)
	summed.Add(byModel["gpt-5.6-luna"])
	assert.Equal(t, m.PerModelUsage["gpt-5.6-luna"], summed, "cross-iteration Add retains reasoning")
}

func TestCodexSubagentUsage_AddOverflowDoesNotPartiallyMutateMetrics(t *testing.T) {
	t.Parallel()
	maxInt := int(^uint(0) >> 1)
	m := &RunMetrics{InputTokens: maxInt, ReasoningTokens: 5,
		PerModelUsage: map[string]ModelUsage{"parent": {Requests: 1, InputTokens: maxInt, ReasoningTokens: 5}}}
	err := addCodexUsage(m, map[string]ModelUsage{"child": {Requests: 1, InputTokens: 1, ReasoningTokens: 3}})
	require.Error(t, err)
	assert.Equal(t, maxInt, m.InputTokens)
	assert.Equal(t, 5, m.ReasoningTokens)
	assert.Equal(t, map[string]ModelUsage{"parent": {Requests: 1, InputTokens: maxInt, ReasoningTokens: 5}}, m.PerModelUsage)
}

func TestCodexSubagentUsage_RootFallbackUsesOnlyRecordedRootResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version string
		want    ModelUsage
	}{
		{"0157", ModelUsage{Requests: 1, InputTokens: 18, OutputTokens: 640, CostUnavailable: true,
			ReasoningTokens: 137, CacheReadInputTokens: 77975, CacheCreationInputTokens: 17730}},
		{"0158", ModelUsage{Requests: 1, InputTokens: 18, OutputTokens: 624, CostUnavailable: true,
			ReasoningTokens: 170, CacheReadInputTokens: 78328, CacheCreationInputTokens: 17845}},
	} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			rootID, rollouts := readRecordedCodexUsage(t, tc.version)
			rollouts = append(rollouts, rollouts[0]) // duplicate root artifact
			got, err := foldCodexRootUsage(rootID, rollouts)
			require.NoError(t, err)
			assert.Equal(t, map[string]ModelUsage{"gpt-5.6-luna": tc.want}, got)
		})
	}
}

func TestCodexSubagentUsage_RootFallbackRejectsConflictingEvidence(t *testing.T) {
	t.Parallel()
	rootID, rollouts := readRecordedCodexUsage(t, "0158")
	_, duplicate := readRecordedCodexUsage(t, "0158")
	duplicate[0].Responses[0].Record.Usage.OutputTokens++
	_, err := foldCodexRootUsage(rootID, append(rollouts, duplicate[0]))
	require.Error(t, err)
	got, err := foldCodexRootUsage("unrelated-root", rollouts)
	require.NoError(t, err)
	assert.Empty(t, got, "missing root evidence must not borrow other sessions' tokens")
}
