package cli

import (
	"encoding/json"
	"testing"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func TestCostAvailability_AggregateAndTelemetry(t *testing.T) {
	var unknown agentruntime.RunMetrics
	require.NoError(t, json.Unmarshal([]byte(`{"cost_unavailable":true,"input_tokens":10,"per_model_usage":{"model":{"cost_unavailable":true,"input_tokens":10}}}`), &unknown))
	known := agentruntime.RunMetrics{TotalCostUSD: 0.25, InputTokens: 5,
		PerModelUsage: map[string]agentruntime.ModelUsage{"model": {CostUSD: 0.25, InputTokens: 5}}}
	for _, unknownFirst := range []bool{false, true} {
		var agg aggregateMetrics
		if unknownFirst {
			aggregateRunMetrics(&agg, &unknown, 1)
			aggregateRunMetrics(&agg, &known, 2)
		} else {
			aggregateRunMetrics(&agg, &known, 1)
			aggregateRunMetrics(&agg, &unknown, 2)
		}
		data, err := json.Marshal(agg)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(data, &fields))
		assert.Equal(t, true, fields["cost_unavailable"])
		assert.Equal(t, true, fields["per_model_usage"].(map[string]any)["model"].(map[string]any)["cost_unavailable"])
		assert.Equal(t, 0.25, agg.TotalCostUSD)
		assert.Equal(t, 15, agg.TokenUsage.Input)
		assertUnknownCostAttrs(t, rootSpanEndAttrs(agg, 2))
	}
	assertUnknownCostAttrs(t, agentSpanEndAttrs(1, 0, "openai", "codex", &unknown))
}

func assertUnknownCostAttrs(t *testing.T, attrs []attribute.KeyValue) {
	t.Helper()
	got := make(map[attribute.Key]attribute.Value, len(attrs))
	for _, attr := range attrs {
		got[attr.Key] = attr.Value
	}
	assert.NotContains(t, got, attribute.Key("fullsend.cost_usd"), "unreported cost must not emit a free or partial-cost measurement")
	assert.Equal(t, attribute.BoolValue(true), got["fullsend.cost_unavailable"])
}

func TestCostAvailability_KnownZeroRemainsAReportedCost(t *testing.T) {
	var agg aggregateMetrics
	data, err := json.Marshal(agg)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "cost_unavailable")
	for _, attrs := range [][]attribute.KeyValue{
		rootSpanEndAttrs(agg, 1),
		agentSpanEndAttrs(1, 0, "anthropic", "claude", &agentruntime.RunMetrics{}),
	} {
		assert.Contains(t, attrs, attribute.Float64("fullsend.cost_usd", 0))
		for _, attr := range attrs {
			assert.NotEqual(t, attribute.Key("fullsend.cost_unavailable"), attr.Key)
		}
	}
}
