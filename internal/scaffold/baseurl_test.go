package scaffold

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHarnessBaseURL(t *testing.T) {
	sha := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	t.Run("valid inputs", func(t *testing.T) {
		url, err := HarnessBaseURL("triage", sha)
		require.NoError(t, err)
		assert.Equal(t,
			"https://raw.githubusercontent.com/fullsend-ai/fullsend/"+sha+"/internal/scaffold/fullsend-repo/harness/triage.yaml",
			url)
	})

	t.Run("hyphenated name", func(t *testing.T) {
		url, err := HarnessBaseURL("my-agent", sha)
		require.NoError(t, err)
		assert.Contains(t, url, "/my-agent.yaml")
	})

	t.Run("invalid harness name uppercase", func(t *testing.T) {
		_, err := HarnessBaseURL("Triage", sha)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid harness name")
	})

	t.Run("invalid harness name empty", func(t *testing.T) {
		_, err := HarnessBaseURL("", sha)
		assert.Error(t, err)
	})

	t.Run("invalid harness name starts with digit", func(t *testing.T) {
		_, err := HarnessBaseURL("1agent", sha)
		assert.Error(t, err)
	})

	t.Run("invalid harness name special chars", func(t *testing.T) {
		_, err := HarnessBaseURL("agent.name", sha)
		assert.Error(t, err)
	})

	t.Run("invalid commit SHA too short", func(t *testing.T) {
		_, err := HarnessBaseURL("triage", "abc123")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid commit SHA")
	})

	t.Run("invalid commit SHA uppercase", func(t *testing.T) {
		_, err := HarnessBaseURL("triage", "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2")
		assert.Error(t, err)
	})

	t.Run("invalid commit SHA wrong length", func(t *testing.T) {
		_, err := HarnessBaseURL("triage", strings.Repeat("a", 39))
		assert.Error(t, err)
	})
}

func TestHarnessContent(t *testing.T) {
	t.Run("invalid name errors", func(t *testing.T) {
		_, err := HarnessContent("INVALID")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid harness name")
	})

	t.Run("unknown harness errors", func(t *testing.T) {
		_, err := HarnessContent("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown harness")
	})
}
