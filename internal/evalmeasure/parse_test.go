package evalmeasure

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTelemetryFile_Complete(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	require.Len(t, traces, 1)
	tr := traces[0]
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", tr.TraceID)
	run, ok := tr.SpanByName("run")
	require.True(t, ok)
	got, ok := run.AttrString("fullsend.agent")
	require.True(t, ok)
	assert.Equal(t, "triage", got)
	cost, ok := run.AttrFloat("fullsend.cost_usd")
	require.True(t, ok)
	assert.InDelta(t, 0.54, cost, 1e-9)
	assert.Len(t, tr.SpansByName("agent"), 1)
	assert.InDelta(t, 6.0, run.DurationSeconds(), 1e-9)
}

func TestParseTelemetryFile_MergesLinesSameTrace(t *testing.T) {
	t.Parallel()
	traces, _, err := ParseTelemetryFile(filepath.Join("testdata", "split.jsonl"))
	require.NoError(t, err)
	require.Len(t, traces, 1)
	assert.Len(t, traces[0].Spans, 3)
	_, ok := traces[0].SpanByName("sandbox_create")
	assert.True(t, ok)
}

func TestParseTelemetryFile_InvalidLineSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("not-json\n"), 0o644))
	traces, stats, err := ParseTelemetryFile(path)
	require.NoError(t, err, "a truncated/corrupt line must not fail the whole file")
	assert.Empty(t, traces)
	assert.Equal(t, 1, stats.NonEmptyLines)
	assert.Equal(t, 1, stats.SkippedLines)
	assert.Equal(t, 0, stats.SkippedSpans)
}

func TestParseTelemetryFile_TruncatedLineDoesNotDiscardFile(t *testing.T) {
	t.Parallel()
	good, err := os.ReadFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	body := string(good)
	if body != "" && body[len(body)-1] != '\n' {
		body += "\n"
	}
	body += "{\"resourceSpans\":[{\"scopeSpans\":[{\"spans\":[{\"traceId\":\"truncated\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	traces, stats, err := ParseTelemetryFile(path)
	require.NoError(t, err)
	require.Len(t, traces, 1)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", traces[0].TraceID)
	assert.GreaterOrEqual(t, stats.SkippedLines, 1)
	assert.Greater(t, stats.NonEmptyLines, stats.SkippedLines)
}

func TestParseTelemetryFile_MissingFile(t *testing.T) {
	t.Parallel()
	_, _, err := ParseTelemetryFile(filepath.Join(t.TempDir(), "missing.jsonl"))
	require.Error(t, err)
}

func TestParseTelemetryFile_BadSpanSkippedKeepsGood(t *testing.T) {
	t.Parallel()
	good, err := os.ReadFile(filepath.Join("testdata", "complete.jsonl"))
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.jsonl")
	// Valid JSON with a non-numeric startTimeUnixNano on one span.
	bad := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"cccccccccccccccccccccccccccccccc","spanId":"9999999999999999","name":"run","startTimeUnixNano":"not-a-number","endTimeUnixNano":"2","attributes":[]}]}]}]}` + "\n"
	require.NoError(t, os.WriteFile(path, append(append([]byte{}, good...), []byte(bad)...), 0o644))
	traces, stats, err := ParseTelemetryFile(path)
	require.NoError(t, err)
	require.Len(t, traces, 1)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", traces[0].TraceID)
	assert.Equal(t, 0, stats.SkippedLines)
	assert.GreaterOrEqual(t, stats.SkippedSpans, 1)
}
