package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertCostUnavailableJSON(t *testing.T, value any, want bool) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	if want {
		assert.Equal(t, true, fields["cost_unavailable"])
	} else {
		assert.NotContains(t, fields, "cost_unavailable", "preserve existing known-cost JSON")
	}
}

func TestCostAvailability_ModelUsageAdd(t *testing.T) {
	var unknown ModelUsage
	require.NoError(t, json.Unmarshal([]byte(`{"cost_unavailable":true,"input_tokens":10}`), &unknown))
	for _, unknownFirst := range []bool{false, true} {
		var got ModelUsage
		known := ModelUsage{CostUSD: 0.25, InputTokens: 5}
		if unknownFirst {
			got.Add(unknown)
			got.Add(known)
		} else {
			got.Add(known)
			got.Add(unknown)
		}
		assertCostUnavailableJSON(t, got, true)
		assert.Equal(t, 0.25, got.CostUSD, "retain the known contribution without calling it a complete total")
		assert.Equal(t, 15, got.InputTokens)
	}
	assertCostUnavailableJSON(t, ModelUsage{}, false)
	assertCostUnavailableJSON(t, &RunMetrics{}, false)
}

func TestCostAvailability_CodexStreamAndChildren(t *testing.T) {
	var metrics RunMetrics
	applyCodexMetrics(&metrics, ResultEvent{InputTokens: 7, CostUnavailable: true})
	assertCostUnavailableJSON(t, &metrics, true)
	rootID, rollouts := readRecordedCodexUsage(t, "0158")
	children, err := foldCodexChildUsage(rootID, rollouts)
	require.NoError(t, err)
	for _, usage := range children {
		assertCostUnavailableJSON(t, usage, true)
	}
	require.NoError(t, addCodexUsage(&metrics, children))
	assertCostUnavailableJSON(t, &metrics, true)
	for _, usage := range metrics.PerModelUsage {
		assertCostUnavailableJSON(t, usage, true)
	}
}
