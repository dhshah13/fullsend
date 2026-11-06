// Package evalmeasure scores agent run traces for online eval measurements.
package evalmeasure

import (
	"fmt"
	"strconv"
)

// Attribute names used by EM-001. Most gen_ai.* keys follow OpenTelemetry
// GenAI semantic conventions. gen_ai.system was renamed to
// gen_ai.provider.name in semconv v1.37.0; modelOK accepts either so
// em-001@1 stays green across the emitter migration. Other upstream
// renames remain an em-001 version bump.
const (
	AttrFullsendAgent          = "fullsend.agent"
	AttrFullsendWorkItemID     = "fullsend.work_item_id"
	AttrGenAIAgentName         = "gen_ai.agent.name"
	AttrGenAISystem            = "gen_ai.system" // deprecated; prefer AttrGenAIProviderName
	AttrGenAIProviderName      = "gen_ai.provider.name"
	AttrGenAIRequestModel      = "gen_ai.request.model"
	AttrGenAIUsageInputTokens  = "gen_ai.usage.input_tokens"
	AttrGenAIUsageOutputTokens = "gen_ai.usage.output_tokens"
	AttrGenAIOperationName     = "gen_ai.operation.name"
)

// Span is a portable view of one OTEL span from run-telemetry.jsonl.
type Span struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	Name          string
	StartUnixNano uint64
	EndUnixNano   uint64
	StatusCode    int // 0 unset, 1 OK, 2 ERROR (OTLP)
	Attrs         map[string]any
}

// Trace groups spans that share a trace id.
type Trace struct {
	TraceID string
	Spans   []Span
}

// EvaluationResult is a portable measurement score (no vendor types).
type EvaluationResult struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Explanation string `json:"explanation"`
	TraceID     string `json:"trace_id"`
	SpanID      string `json:"span_id"`
	WorkItemID  string `json:"work_item_id,omitempty"`
	Agent       string `json:"agent"`
	Version     string `json:"version"`
	// Value is the numeric score. Skip rows leave this at 0; consumers
	// must key off Label, not Value==0. Do not add omitempty: a real
	// fail can also be 0.0.
	Value float64 `json:"value"`
}

// AttrString returns a string attribute.
func (s Span) AttrString(key string) (string, bool) {
	v, ok := s.Attrs[key]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	default:
		return fmt.Sprint(t), true
	}
}

// AttrBool returns a bool attribute.
func (s Span) AttrBool(key string) (bool, bool) {
	v, ok := s.Attrs[key]
	if !ok || v == nil {
		return false, false
	}
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch t {
		case "true":
			return true, true
		case "false":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

// AttrInt returns an int64 attribute.
func (s Span) AttrInt(key string) (int64, bool) {
	v, ok := s.Attrs[key]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// AttrFloat returns a float64 attribute.
func (s Span) AttrFloat(key string) (float64, bool) {
	v, ok := s.Attrs[key]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case string:
		n, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// DurationSeconds returns end-start in seconds.
func (s Span) DurationSeconds() float64 {
	if s.EndUnixNano <= s.StartUnixNano {
		return 0
	}
	return float64(s.EndUnixNano-s.StartUnixNano) / 1e9
}

// SpanByName returns the first span with the given name.
func (t Trace) SpanByName(name string) (Span, bool) {
	for _, s := range t.Spans {
		if s.Name == name {
			return s, true
		}
	}
	return Span{}, false
}

// SpansByName returns all spans with the given name.
func (t Trace) SpansByName(name string) []Span {
	var out []Span
	for _, s := range t.Spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// AgentName returns the agent identity from run or agent spans.
func (t Trace) AgentName() string {
	if run, ok := t.SpanByName("run"); ok {
		if a, ok := run.AttrString(AttrFullsendAgent); ok && a != "" {
			return a
		}
		if a, ok := run.AttrString(AttrGenAIAgentName); ok && a != "" {
			return a
		}
	}
	for _, a := range t.SpansByName("agent") {
		if name, ok := a.AttrString(AttrGenAIAgentName); ok && name != "" {
			return name
		}
	}
	return ""
}
