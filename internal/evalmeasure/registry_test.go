package evalmeasure

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRegistry_Valid(t *testing.T) {
	t.Parallel()
	reg, err := LoadRegistry(filepath.Join("testdata", "sample-registry.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "triage", reg.Agent)
	require.Len(t, reg.Measurements, 1)
	assert.Equal(t, "em-001", reg.Measurements[0].ID)
	assert.Equal(t, "trace_fitness", reg.Measurements[0].Scorer)
	assert.Equal(t, 1, reg.Measurements[0].Version)
}

func TestLoadRegistry_MissingAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("measurements:\n  - id: em-001\n    scorer: trace_fitness\n    version: 1\n"), 0o644))
	_, err := LoadRegistry(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent is required")
}

func TestLoadRegistry_MissingID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent: test\nmeasurements:\n  - scorer: trace_fitness\n    version: 1\n"), 0o644))
	_, err := LoadRegistry(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")
}

func TestLoadRegistry_MissingScorer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent: test\nmeasurements:\n  - id: em-001\n    version: 1\n"), 0o644))
	_, err := LoadRegistry(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scorer is required")
}

func TestLoadRegistry_ZeroVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent: test\nmeasurements:\n  - id: em-001\n    scorer: trace_fitness\n    version: 0\n"), 0o644))
	_, err := LoadRegistry(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version must be >= 1")
}

func TestLoadRegistry_PipeInID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent: test\nmeasurements:\n  - id: \"em|001\"\n    scorer: trace_fitness\n    version: 1\n"), 0o644))
	_, err := LoadRegistry(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain pipe or newline")
}

func TestLoadRegistry_InvalidYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("{{invalid yaml"), 0o644))
	_, err := LoadRegistry(path)
	require.Error(t, err)
}

func TestLoadRegistry_FileNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadRegistry("/nonexistent/path.yaml")
	require.Error(t, err)
}
