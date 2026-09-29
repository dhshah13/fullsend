package runtime

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexUsageBoundary_RejectsAmbiguousSessionAndModelEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data, problem string
	}{
		{"duplicate session", codexUsageTestMeta + "\n" + codexUsageTestMeta, "multiple session identities"},
		{"missing thread", `{"type":"session_meta","payload":{"session_id":"root"}}`, "invalid session identity"},
		{"missing session", `{"type":"session_meta","payload":{"id":"child"}}`, "invalid session identity"},
		{"malformed identity", `{"type":"session_meta","payload":{"id":17,"session_id":"root"}}`, "invalid session identity"},
		{"no identity", `{"type":"turn_context","payload":{"turn_id":"turn","model":"gpt-5.6-luna"}}`, "missing session identity"},
		{"malformed context", codexUsageTestMeta + "\n" + `{"type":"turn_context","payload":{"turn_id":42}}`, "invalid turn context"},
		{"conflicting models", codexUsageTestMeta + "\n" + codexUsageTestContext("turn", "gpt-5.6-luna") + "\n" + codexUsageTestContext("turn", "gpt-5.6-sol"), "conflicting models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseCodexRolloutUsage(strings.NewReader(tc.data))
			require.ErrorContains(t, err, tc.problem, "ambiguous evidence must never select a session or model by file order")
		})
	}
}

func TestCodexUsageBoundary_UnidentifiedContextCannotStealAttribution(t *testing.T) {
	t.Parallel()
	data := strings.Join([]string{
		codexUsageTestMeta,
		codexUsageTestContext("turn", "openai/gpt-5.6-luna"),
		codexUsageTestContext("turn", "gpt-5.6-luna"),
		codexUsageTestContext("", "gpt-5.6-sol"),
		codexUsageTestRecord(t, "response", "turn", codexUsage{
			InputTokens: 15, CachedInputTokens: 6, CacheWriteInputTokens: 4,
			OutputTokens: 9, ReasoningOutputTokens: 3,
		}),
	}, "\n")
	rollout, err := parseCodexRolloutUsage(strings.NewReader(data))
	require.NoError(t, err)
	got, err := foldCodexChildUsage("root", []codexRolloutUsage{rollout})
	require.NoError(t, err)
	assert.Equal(t, map[string]ModelUsage{"gpt-5.6-luna": {
		Requests: 1, InputTokens: 5, OutputTokens: 6, ReasoningTokens: 3,
		CacheReadInputTokens: 6, CacheCreationInputTokens: 4, CostUnavailable: true,
	}}, got)
}

type codexUsageReadFailure struct{ err error }

func (r codexUsageReadFailure) Read([]byte) (int, error) { return 0, r.err }

func TestCodexUsageBoundary_ReadFailureCannotBecomeCompleteEvidence(t *testing.T) {
	t.Parallel()
	brokenRead := errors.New("rollout storage disconnected")
	data := codexUsageTestMeta + "\n" + codexUsageTestRecord(t, "response", "turn", codexUsage{InputTokens: 9}) + "\n"
	_, err := parseCodexRolloutUsage(io.MultiReader(strings.NewReader(data), codexUsageReadFailure{brokenRead}))
	require.ErrorIs(t, err, brokenRead, "a valid prefix must not hide a missing tail")
}

func TestCodexUsageBoundary_ReducerRequiresRoot(t *testing.T) {
	t.Parallel()
	// Identity and usage are validated by parseCodexRolloutUsage (see
	// TestCodexSubagentUsage_RejectsIncompleteResponseIdentityAndJSON and
	// RejectsMalformedUsage); the fold only needs the root.
	_, err := foldCodexChildUsage("", nil)
	require.Error(t, err, "a missing root must not accept unrelated sessions as zero usage")
	_, err = foldCodexRootUsage("", nil)
	require.Error(t, err)
}

func TestCodexUsageBoundary_UnknownModelDeduplicatesAndConservesUsage(t *testing.T) {
	t.Parallel()
	rollout, err := parseCodexRolloutUsage(strings.NewReader(strings.Join([]string{
		codexUsageTestMeta,
		codexUsageTestContext("turn", "  "),
		codexUsageTestRecord(t, "one", "turn", codexUsage{InputTokens: 15, CachedInputTokens: 6, CacheWriteInputTokens: 4, OutputTokens: 9, ReasoningOutputTokens: 3}),
		codexUsageTestRecord(t, "two", "turn", codexUsage{InputTokens: 10, CachedInputTokens: 2, CacheWriteInputTokens: 1, OutputTokens: 4, ReasoningOutputTokens: 1}),
	}, "\n")))
	require.NoError(t, err)
	got, err := foldCodexChildUsage("root", []codexRolloutUsage{rollout, rollout})
	require.NoError(t, err)
	assert.Equal(t, map[string]ModelUsage{"unknown": {
		Requests: 1, InputTokens: 12, OutputTokens: 9, ReasoningTokens: 4,
		CacheReadInputTokens: 8, CacheCreationInputTokens: 5, CostUnavailable: true,
	}}, got, "two responses plus a copied artifact are one invocation and 38 total tokens")
}

func TestCodexUsageBoundary_PerModelOverflowIsAtomic(t *testing.T) {
	t.Parallel()
	maxInt := int(^uint(0) >> 1)
	metrics := &RunMetrics{InputTokens: 2, TotalCostUSD: 1.25,
		PerModelUsage: map[string]ModelUsage{"parent": {Requests: maxInt, InputTokens: 2, CostUSD: 1.25}}}
	err := addCodexUsage(metrics, map[string]ModelUsage{"parent": {Requests: 1, InputTokens: 3, CostUnavailable: true}})
	require.Error(t, err, "invocation overflow must be rejected even when the aggregate token counters fit")
	assert.Equal(t, 2, metrics.InputTokens)
	assert.Equal(t, 1.25, metrics.TotalCostUSD)
	assert.False(t, metrics.CostUnavailable, "a failed merge must not change known-cost status")
	assert.Equal(t, map[string]ModelUsage{"parent": {Requests: maxInt, InputTokens: 2, CostUSD: 1.25}}, metrics.PerModelUsage)
	require.Error(t, addCodexUsage(nil, nil), "missing metrics must return an error instead of panic")
}

func TestCodexUsageBoundary_UnknownChildCostPreservesKnownContribution(t *testing.T) {
	t.Parallel()
	metrics := &RunMetrics{InputTokens: 2, TotalCostUSD: 1.25,
		PerModelUsage: map[string]ModelUsage{"parent": {Requests: 1, InputTokens: 2, CostUSD: 1.25}}}
	require.NoError(t, addCodexUsage(metrics, map[string]ModelUsage{"child": {
		Requests: 1, InputTokens: 3, OutputTokens: 4, ReasoningTokens: 2, CostUnavailable: true,
	}}))
	assert.Equal(t, 5, metrics.InputTokens)
	assert.Equal(t, 4, metrics.OutputTokens)
	assert.Equal(t, 2, metrics.ReasoningTokens)
	assert.Equal(t, 1.25, metrics.TotalCostUSD, "partial known dollars remain useful, but are not the complete price")
	assert.True(t, metrics.CostUnavailable)
	assert.Equal(t, map[string]ModelUsage{
		"parent": {Requests: 1, InputTokens: 2, CostUSD: 1.25},
		"child":  {Requests: 1, InputTokens: 3, OutputTokens: 4, ReasoningTokens: 2, CostUnavailable: true},
	}, metrics.PerModelUsage)
}
