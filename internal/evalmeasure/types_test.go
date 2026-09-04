package evalmeasure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAttrString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		attrs  map[string]any
		key    string
		want   string
		wantOK bool
	}{
		{"string value", map[string]any{"k": "v"}, "k", "v", true},
		{"int value coerced", map[string]any{"k": 42}, "k", "42", true},
		{"missing key", map[string]any{}, "k", "", false},
		{"nil value", map[string]any{"k": nil}, "k", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := Span{Attrs: tt.attrs}
			got, ok := s.AttrString(tt.key)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAttrInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		attrs  map[string]any
		key    string
		want   int64
		wantOK bool
	}{
		{"int64", map[string]any{"k": int64(42)}, "k", 42, true},
		{"int", map[string]any{"k": 42}, "k", 42, true},
		{"float64", map[string]any{"k": float64(42)}, "k", 42, true},
		{"string parseable", map[string]any{"k": "42"}, "k", 42, true},
		{"string unparseable", map[string]any{"k": "abc"}, "k", 0, false},
		{"missing key", map[string]any{}, "k", 0, false},
		{"nil value", map[string]any{"k": nil}, "k", 0, false},
		{"bool not int", map[string]any{"k": true}, "k", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := Span{Attrs: tt.attrs}
			got, ok := s.AttrInt(tt.key)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAttrFloat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		attrs  map[string]any
		key    string
		want   float64
		wantOK bool
	}{
		{"float64", map[string]any{"k": float64(3.14)}, "k", 3.14, true},
		{"int64", map[string]any{"k": int64(42)}, "k", 42, true},
		{"int", map[string]any{"k": 42}, "k", 42, true},
		{"string parseable", map[string]any{"k": "3.14"}, "k", 3.14, true},
		{"string unparseable", map[string]any{"k": "abc"}, "k", 0, false},
		{"missing key", map[string]any{}, "k", 0, false},
		{"nil value", map[string]any{"k": nil}, "k", 0, false},
		{"bool not float", map[string]any{"k": true}, "k", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := Span{Attrs: tt.attrs}
			got, ok := s.AttrFloat(tt.key)
			assert.Equal(t, tt.wantOK, ok)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestDurationSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start uint64
		end   uint64
		want  float64
	}{
		{"normal", 1000000000, 7000000000, 6.0},
		{"zero duration", 100, 100, 0},
		{"end before start", 200, 100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := Span{StartUnixNano: tt.start, EndUnixNano: tt.end}
			assert.InDelta(t, tt.want, s.DurationSeconds(), 1e-9)
		})
	}
}

func TestSpanByName(t *testing.T) {
	t.Parallel()
	tr := Trace{
		Spans: []Span{
			{Name: "run"},
			{Name: "agent"},
		},
	}
	s, ok := tr.SpanByName("run")
	assert.True(t, ok)
	assert.Equal(t, "run", s.Name)

	_, ok = tr.SpanByName("missing")
	assert.False(t, ok)
}

func TestSpansByName(t *testing.T) {
	t.Parallel()
	tr := Trace{
		Spans: []Span{
			{Name: "agent"},
			{Name: "run"},
			{Name: "agent"},
		},
	}
	assert.Len(t, tr.SpansByName("agent"), 2)
	assert.Empty(t, tr.SpansByName("missing"))
}

func TestAgentName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		spans []Span
		want  string
	}{
		{
			"from run fullsend.agent",
			[]Span{{Name: "run", Attrs: map[string]any{"fullsend.agent": "triage"}}},
			"triage",
		},
		{
			"from run gen_ai.agent.name",
			[]Span{{Name: "run", Attrs: map[string]any{"gen_ai.agent.name": "review"}}},
			"review",
		},
		{
			"from agent span",
			[]Span{
				{Name: "run", Attrs: map[string]any{}},
				{Name: "agent", Attrs: map[string]any{"gen_ai.agent.name": "code"}},
			},
			"code",
		},
		{
			"empty when no identity",
			[]Span{{Name: "run", Attrs: map[string]any{}}},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := Trace{Spans: tt.spans}
			assert.Equal(t, tt.want, tr.AgentName())
		})
	}
}

func TestAttrBool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		attrs  map[string]any
		key    string
		want   bool
		wantOK bool
	}{
		{"true", map[string]any{"k": true}, "k", true, true},
		{"false", map[string]any{"k": false}, "k", false, true},
		{"string true", map[string]any{"k": "true"}, "k", true, true},
		{"string false", map[string]any{"k": "false"}, "k", false, true},
		{"string other", map[string]any{"k": "yes"}, "k", false, false},
		{"missing key", map[string]any{}, "k", false, false},
		{"nil value", map[string]any{"k": nil}, "k", false, false},
		{"int not bool", map[string]any{"k": 1}, "k", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := Span{Attrs: tt.attrs}
			got, ok := s.AttrBool(tt.key)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
