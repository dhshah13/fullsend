package runtime

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestCodexRunBoundary_RejectsMissingMandatoryAndUnexpectedSharedHooks(t *testing.T) {
	for _, tc := range []struct {
		name           string
		shared, digest bool
		unexpected     bool
	}{
		{name: "mandatory policy missing without shared scanners"},
		{name: "mandatory policy missing with shared scanners", shared: true},
		{name: "shared scanners recorded despite runner disabling them", digest: true, unexpected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := CodexRuntime{}
			store, logPath := t.TempDir(), filepath.Join(t.TempDir(), "openshell.log")
			var hooks *codexHooksManifest
			if tc.shared {
				hooks = codexHooksManifestFor(r.codexHooksDir(), security.SandboxHookConfigFromHarness(&harness.Harness{}))
			}
			seedCodexManifest(t, store, r, hooks)
			fakeOpenshellCodex(t, logPath, store, "codex-cli 0.158.0")
			digests, ok := lookupRunnerHeldDigests("sb")
			require.True(t, ok)
			if !tc.digest {
				digests.HooksJSON = ""
			}
			if tc.unexpected {
				digests.HookScripts = map[string]string{}
			}
			recordRunnerHeldDigests("sb", digests)
			params := RunParams{SandboxName: "sb", Model: "gpt-5.6-luna", RepoDir: "/sandbox/workspace/repo", Timeout: time.Minute}
			if tc.shared {
				params.HooksSettingsPath = r.codexHooksPath()
			}
			var events []AgentEvent
			params.OnEvent = func(event AgentEvent) { events = append(events, event) }
			metrics := &RunMetrics{}
			exit, err := r.Run(t.Context(), params, ui.New(&bytes.Buffer{}), time.Now(), metrics)
			require.ErrorContains(t, err, "hook wiring is inconsistent")
			assert.Equal(t, -1, exit)
			assert.Empty(t, events, "integrity failure must precede a model run or result event")
			assert.Zero(t, metrics.NumTurns)
			assert.Zero(t, metrics.InputTokens)
			assert.NotContains(t, readFileString(t, logPath), "exec --json", "inconsistent policy must never launch inference")
		})
	}
}

// The launcher exits 78 before Codex starts; a retry cannot succeed.
func TestCodexRunBoundary_WriteProtectionFailureIsAnError(t *testing.T) {
	r := CodexRuntime{}
	store := t.TempDir()
	seedCodexManifest(t, store, r, nil)
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), store, "codex-cli 0.158.0")
	t.Setenv("FULLSEND_TEST_RUN_EXIT", "78")
	exit, err := r.Run(t.Context(), RunParams{SandboxName: "sb", Model: "gpt-5.6-luna", RepoDir: "/sandbox/workspace/repo", Timeout: time.Minute},
		ui.New(&bytes.Buffer{}), time.Now(), &RunMetrics{})
	require.ErrorContains(t, err, "Codex runs require Linux Landlock ABI 3+")
	assert.Equal(t, codexWriteProtectionExit, exit)
}

func TestCodexRunBoundary_UsageCollectionFailureRetainsKnownStreamCounters(t *testing.T) {
	r := CodexRuntime{}
	require.Error(t, r.collectCodexUsage(t.Context(), "unused", "root", nil, nil, true))
	missing := filepath.Join(t.TempDir(), "missing-parent")
	t.Setenv("TMPDIR", missing)
	metrics := &RunMetrics{Model: "root-model", NumTurns: 1, InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2}
	err := r.collectCodexUsage(t.Context(), "unused", "root", metrics, nil, true)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, 1, metrics.NumTurns)
	assert.Equal(t, 20, metrics.InputTokens)
	assert.Equal(t, 8, metrics.OutputTokens)
	assert.Equal(t, 2, metrics.ReasoningTokens)
	assert.True(t, metrics.CostUnavailable, "missing raw evidence must not turn an unpriced run into a known zero-dollar run")
	assert.Equal(t, map[string]ModelUsage{"root-model": {
		Requests: 1, InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2, CostUnavailable: true,
	}}, metrics.PerModelUsage)
}

func TestCodexRunBoundary_LostDownloadedRootRetainsStreamAndChildUsage(t *testing.T) {
	child := strings.Join([]string{codexUsageTestMeta, codexUsageTestContext("turn", "child-model"),
		codexUsageTestRecord(t, "child-response", "turn", codexUsage{
			InputTokens: 15, CachedInputTokens: 6, CacheWriteInputTokens: 4,
			OutputTokens: 9, ReasoningOutputTokens: 3,
		}),
	}, "\n")
	fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "", map[string]string{
		"a-root.jsonl":  testCodexRootUsage("root", "root-model", 100, 20, 5),
		"b-child.jsonl": child,
	})
	// Inject a host storage loss between transfers, after the root file was
	// accepted by the downloader. The collector must surface the lost file
	// while still parsing the second, intact child; no timing race is needed.
	fake, err := exec.LookPath("openshell")
	require.NoError(t, err)
	require.NoError(t, os.Rename(fake, fake+"-with-bodies"))
	script := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in\n" +
		"*b-child.jsonl*fullsend-codex-rollout*) find \"$FULLSEND_TEST_USAGE_TEMP\" -type f -name '*a-root.jsonl' -delete;;\n" +
		"esac\nexec " + shellQuote(fake+"-with-bodies") + " \"$@\"\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))
	privateRoot := t.TempDir()
	t.Setenv("FULLSEND_TEST_USAGE_TEMP", privateRoot)
	t.Setenv("TMPDIR", privateRoot)
	t.Cleanup(func() { clearCodexTranscriptIdentities("lost-root") })
	metrics := &RunMetrics{Model: "root-model", NumTurns: 1, InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2}
	err = (CodexRuntime{}).collectCodexUsage(t.Context(), "lost-root", "root", metrics, map[string]bool{"child": true}, true)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorContains(t, err, "root has no collected response usage")
	assert.Equal(t, 1, metrics.NumTurns, "recovered accounting does not invent completion")
	assert.Equal(t, 25, metrics.InputTokens, "retain the stream's 20 plus the child's 5; never use the unavailable raw 100")
	assert.Equal(t, 14, metrics.OutputTokens)
	assert.Equal(t, 5, metrics.ReasoningTokens)
	assert.Equal(t, 6, metrics.CacheReadInputTokens)
	assert.Equal(t, 4, metrics.CacheCreationInputTokens)
	assert.True(t, metrics.CostUnavailable)
	assert.Equal(t, map[string]ModelUsage{
		"root-model":  {Requests: 1, InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2, CostUnavailable: true},
		"child-model": {Requests: 1, InputTokens: 5, OutputTokens: 6, ReasoningTokens: 3, CacheReadInputTokens: 6, CacheCreationInputTokens: 4, CostUnavailable: true},
	}, metrics.PerModelUsage)
}
