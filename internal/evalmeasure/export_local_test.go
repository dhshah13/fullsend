package evalmeasure

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendMeasurements_EmptySlice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")
	require.NoError(t, AppendMeasurements(path, nil))
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "file should not be created for empty slice")
}

func TestAppendMeasurements_CreatesParentDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "out.jsonl")
	err := AppendMeasurements(path, []EvaluationResult{{Name: "test", Value: 1}})
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"name":"test"`)
}

func TestAlreadyScored_NonExistentLedger(t *testing.T) {
	t.Parallel()
	done, err := AlreadyScored("/nonexistent/ledger.jsonl", "trace1", "eval1", "v1")
	require.NoError(t, err)
	assert.False(t, done)
}

func TestRecordScored_ThenAlreadyScored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger.jsonl")

	done, err := AlreadyScored(ledger, "trace1", "eval1", "v1")
	require.NoError(t, err)
	assert.False(t, done)

	require.NoError(t, RecordScored(ledger, "trace1", "eval1", "v1"))

	done, err = AlreadyScored(ledger, "trace1", "eval1", "v1")
	require.NoError(t, err)
	assert.True(t, done)

	done, err = AlreadyScored(ledger, "trace2", "eval1", "v1")
	require.NoError(t, err)
	assert.False(t, done, "different trace should not match")
}

func TestRecordScored_CreatesParentDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ledger := filepath.Join(dir, "sub", "ledger.jsonl")
	require.NoError(t, RecordScored(ledger, "trace1", "eval1", "v1"))
	b, err := os.ReadFile(ledger)
	require.NoError(t, err)
	assert.Contains(t, string(b), "trace1|eval1|v1")
}
