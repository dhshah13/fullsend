package runtime

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexRuntimeMetadata(t *testing.T) {
	t.Parallel()

	rt := CodexRuntime{}
	assert.Equal(t, "codex", rt.Name())
	// Single-vendor runtime: the gen_ai.system is the model vendor, not the
	// runtime name (pi and opencode are multi-provider and use their own).
	assert.Equal(t, "openai", rt.System())
	assert.Equal(t, sandbox.SandboxCodexConfig, rt.ConfigDir())
	assert.Equal(t, sandbox.SandboxWorkspace, rt.WorkspaceDir())
	assert.Equal(t, []string{"export CODEX_HOME=" + sandbox.SandboxCodexConfig}, rt.EnvExports())
	assert.Equal(t, "codex-debug.log", rt.DebugLogName())
}

func TestCodexRuntimeRun_NotImplemented(t *testing.T) {
	t.Parallel()

	rt := CodexRuntime{}
	exit, err := rt.Run(context.Background(), RunParams{}, nil, time.Now(), nil)
	assert.Equal(t, -1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
	assert.Contains(t, err.Error(), "#6920")
}

func TestCodexRuntimeBootstrap_NotImplemented(t *testing.T) {
	t.Parallel()

	rt := CodexRuntime{}
	err := rt.Bootstrap(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
	assert.Contains(t, err.Error(), "#6920")
}

func TestCodexRuntimeExtractStubs_NotImplemented(t *testing.T) {
	t.Parallel()

	rt := CodexRuntime{}
	err := rt.ExtractTranscripts("", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "#6920")

	err = rt.ExtractDebugLog("", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "#6920")
}

func TestCodexRuntimeNoopMethods(t *testing.T) {
	t.Parallel()

	rt := CodexRuntime{}
	assert.Nil(t, rt.ParseTranscriptErrors(""))
	assert.NoError(t, rt.ClearIterationArtifacts(""))

	te, ok := rt.ParseTranscriptFile("")
	assert.False(t, ok)
	assert.Equal(t, TranscriptError{}, te)

	var buf bytes.Buffer
	rt.EmitTranscriptErrors(&buf, nil)
}
