package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProviderArgsLiteral_NoExpansion(t *testing.T) {
	t.Setenv("LEAK", "expanded")
	credentials := map[string]string{
		"OPENAI_API_KEY": "tok-${LEAK}-$LEAK-literal",
		"ZERO":           "",
	}

	args, extraEnv, secrets := buildProviderArgsLiteral("openai-abc", "fullsend-openai", credentials)

	assert.Equal(t, []string{"provider", "create", "--name", "openai-abc", "--type", "fullsend-openai",
		"--credential", "OPENAI_API_KEY", "--credential", "ZERO="}, args, "bare-key form; empty value uses inline KEY=")
	assert.Equal(t, []string{"OPENAI_API_KEY=tok-${LEAK}-$LEAK-literal"}, extraEnv, "value passes through verbatim, `$` intact")
	assert.Equal(t, []string{"tok-${LEAK}-$LEAK-literal"}, secrets)
	for _, a := range args {
		assert.NotContains(t, a, "tok-", "the value never appears on the command line")
	}
}

func TestBuildProviderUpdateArgsLiteral(t *testing.T) {
	args := buildProviderUpdateArgsLiteral("openai-abc", map[string]string{"OPENAI_API_KEY": "v", "B": ""})
	assert.Equal(t, []string{"provider", "update", "openai-abc", "--credential", "B=", "--credential", "OPENAI_API_KEY"}, args)
}

// recordingOpenshell installs an openshell stub on PATH that logs each
// invocation's arguments and the value of OPENAI_API_KEY in its environment,
// and exits with exitCode after printing stdoutText.
func recordingOpenshell(t *testing.T, stdoutText string, exitCode int) (argsLog, envLog string) {
	t.Helper()
	binDir := t.TempDir()
	argsLog = filepath.Join(binDir, "args.log")
	envLog = filepath.Join(binDir, "env.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(argsLog) + "\n" +
		"printf '%s\\n' \"${OPENAI_API_KEY-<unset>}\" >> " + shellQuote(envLog) + "\n" +
		"printf '%s' " + shellQuote(stdoutText) + "\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsLog, envLog
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestEnsureProviderLiteral_CreatesWithValueInChildEnvOnly(t *testing.T) {
	argsLog, envLog := recordingOpenshell(t, "", 0)
	t.Setenv("LEAK", "expanded")

	err := EnsureProviderLiteral(context.Background(), "openai-abc", "fullsend-openai", map[string]string{"OPENAI_API_KEY": "tok-$LEAK-9f8e7d"})
	require.NoError(t, err)

	args := readLines(t, argsLog)
	require.Len(t, args, 1)
	assert.Equal(t, "provider create --name openai-abc --type fullsend-openai --credential OPENAI_API_KEY", args[0])
	env := readLines(t, envLog)
	assert.Equal(t, "tok-$LEAK-9f8e7d", env[0], "unexpanded value in the child environment")
}

func TestEnsureProviderLiteral_ErrorRedactsValue(t *testing.T) {
	_, _ = recordingOpenshell(t, "boom: tok-secret-value-9f8e7d rejected", 1)

	err := EnsureProviderLiteral(context.Background(), "openai-abc", "fullsend-openai", map[string]string{"OPENAI_API_KEY": "tok-secret-value-9f8e7d"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "tok-secret-value-9f8e7d")
	assert.Contains(t, err.Error(), "***")
}

func TestSetProviderCredentialExpiry(t *testing.T) {
	argsLog, _ := recordingOpenshell(t, "", 0)
	at := time.Date(2026, 8, 27, 20, 30, 0, 0, time.FixedZone("plus2", 2*3600))

	require.NoError(t, SetProviderCredentialExpiry(context.Background(), "openai-abc", "OPENAI_API_KEY", at))

	args := readLines(t, argsLog)
	require.Len(t, args, 1)
	assert.Equal(t, "provider update openai-abc --credential-expires-at OPENAI_API_KEY=2026-08-27T18:30:00Z", args[0], "RFC3339 in UTC")
}

func TestSetProviderCredentialExpiry_Error(t *testing.T) {
	_, _ = recordingOpenshell(t, "no such provider", 1)
	err := SetProviderCredentialExpiry(context.Background(), "openai-abc", "OPENAI_API_KEY", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openai-abc")
	assert.Contains(t, err.Error(), "no such provider")
}

func TestDeleteProvider(t *testing.T) {
	t.Run("deletes", func(t *testing.T) {
		argsLog, _ := recordingOpenshell(t, "", 0)
		require.NoError(t, DeleteProvider("openai-abc"))
		assert.Equal(t, []string{"provider delete openai-abc"}, readLines(t, argsLog))
	})
	t.Run("already gone is not an error", func(t *testing.T) {
		// What the 0.0.83 CLI actually prints for a missing provider (exit 0).
		_, _ = recordingOpenshell(t, "! Provider openai-abc not found", 0)
		require.NoError(t, DeleteProvider("openai-abc"))
		// Defensive variant: the same text with a non-zero exit.
		_, _ = recordingOpenshell(t, "error: provider openai-abc not found", 1)
		require.NoError(t, DeleteProvider("openai-abc"))
	})
	t.Run("other failures surface", func(t *testing.T) {
		_, _ = recordingOpenshell(t, "gateway unreachable", 1)
		err := DeleteProvider("openai-abc")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gateway unreachable")
	})
}
