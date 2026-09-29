package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

const recordedRoot0158 = "01a0e868-0083-7571-b0e2-de21c3a6c910"
const recordedAlpha0158 = "01a0e868-0ff5-7b90-8852-e23f529ca223"

func testCodexRootUsage(rootID, model string, input, output, reasoning int) string {
	return fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"session_id":%q}}
{"type":"turn_context","payload":{"turn_id":"root-turn","model":%q}}
{"type":"token_usage_record","payload":{"thread_id":%q,"session_id":%q,"turn_id":"root-turn","root_turn_id":"root-turn","response_id":"root-response","usage":{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":%d}}}
`, rootID, rootID, model, rootID, rootID, input, output, reasoning)
}

// Give each native session its own file, as the real CLI does. The regular
// openshell stub still handles bootstrap, the exec stream and file discovery.
func fakeOpenshellCodexWithRollouts(t *testing.T, logPath, storeDir, stream string, bodies map[string]string) {
	t.Helper()
	var names, paths []string
	for name := range bodies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		paths = append(paths, CodexRuntime{}.codexSessionsDir()+"/"+name)
	}
	fakeOpenshellCodex(t, logPath, storeDir, "codex-cli 0.158.0", stream, strings.Join(paths, "\n"))
	fake, err := exec.LookPath("openshell")
	require.NoError(t, err)
	require.NoError(t, os.Rename(fake, fake+"-base"))
	script := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in\n"
	for _, name := range names {
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, []byte(bodies[name]), 0o600))
		script += "  *" + shellQuote("/"+name) + "*fullsend-codex-rollout*) cat " + shellQuote(path) + "; exit $?;;\n"
	}
	script += "esac\nexec " + shellQuote(fake+"-base") + " \"$@\"\n"
	require.NoError(t, os.WriteFile(fake, []byte(script), 0o755))
}

func testBasicCodexRootUsage() string {
	root := testCodexRootUsage("01a0da3c-7f1d-7342-84df-2e3c0c50aa08", "gpt-5.6-luna", 33237, 251, 20)
	root = strings.Replace(root, `"cached_input_tokens":0`, `"cached_input_tokens":22013`, 1)
	return strings.Replace(root, `"cache_write_input_tokens":0`, `"cache_write_input_tokens":11215`, 1)
}

func TestCodexCollection_RunRequiresRootEvidence(t *testing.T) {
	complete := testCodexRootUsage(recordedRoot0158, "gpt-5.6-luna", 20, 10, 2)
	metadata := strings.SplitN(complete, "\n", 2)[0]
	for _, tc := range []struct {
		name, body, problem                            string
		omitIdentity, failStart, zeroUsage, zeroStream bool
	}{
		{name: "missing thread identity", omitIdentity: true, problem: "root thread identity"},
		{name: "missing root rollout", problem: "root has no collected response usage"},
		{name: "zero-token success still requires evidence", zeroStream: true, problem: "root has no collected response usage"},
		{name: "metadata without response", body: metadata, problem: "root has no collected response usage"},
		{name: "wrong root session", body: strings.ReplaceAll(complete, recordedRoot0158, "unrelated-root"), problem: "root has no collected response usage"},
		{name: "root with a parent", body: strings.Replace(complete, `"session_id":`, `"parent_thread_id":"other","session_id":`, 1), problem: "root has no collected response usage"},
		{name: "copied child response", body: strings.Replace(complete, `"thread_id":"`+recordedRoot0158+`"`, `"thread_id":"other"`, 1), problem: "root has no collected response usage"},
		{name: "truncated native root", body: complete + `{"type":`, problem: "not JSON"},
		{name: "complete native root", body: complete},
		{name: "reported zero usage", body: testCodexRootUsage(recordedRoot0158, "gpt-5.6-luna", 0, 0, 0), zeroUsage: true},
		{name: "failed startup keeps provider error", body: metadata, failStart: true},
		{name: "failed startup without identity", failStart: true, omitIdentity: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := fmt.Sprintf("{\"type\":\"thread.started\",\"thread_id\":%q}\n", recordedRoot0158)
			if tc.omitIdentity {
				stream = ""
			}
			stream += "{\"type\":\"turn.started\"}\n"
			if tc.failStart {
				stream += "{\"type\":\"turn.failed\",\"error\":{\"message\":\"provider authentication failed\"}}\n"
			} else {
				stream += "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":20,\"cached_input_tokens\":0,\"output_tokens\":10,\"reasoning_output_tokens\":2}}\n"
				if tc.zeroStream {
					stream = strings.NewReplacer(`"input_tokens":20`, `"input_tokens":0`, `"output_tokens":10`, `"output_tokens":0`, `"reasoning_output_tokens":2`, `"reasoning_output_tokens":0`).Replace(stream)
				}
			}
			fixture := filepath.Join(t.TempDir(), "stream.jsonl")
			require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o600))
			store := t.TempDir()
			seedCodexManifest(t, store, CodexRuntime{}, nil)
			listing := ""
			if tc.body != "" {
				listing = CodexRuntime{}.codexSessionsDir() + "/root.jsonl"
			}
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), store, "codex-cli 0.158.0", fixture, listing)
			t.Setenv("FULLSEND_TEST_DOWNLOAD_BODY", tc.body)
			var results []ResultEvent
			metrics := &RunMetrics{}
			exit, err := (CodexRuntime{}).Run(t.Context(), RunParams{
				SandboxName: "sb", RepoDir: "/sandbox/workspace/repo", Model: "gpt-5.6-luna", Timeout: time.Minute,
				OnEvent: func(event AgentEvent) {
					if result, ok := event.(ResultEvent); ok {
						results = append(results, result)
					}
				},
			}, ui.New(&bytes.Buffer{}), time.Now(), metrics)
			require.Len(t, results, 1)
			if tc.problem != "" {
				require.ErrorIs(t, err, ErrIncompleteEvidence)
				assert.Contains(t, err.Error(), tc.problem)
				assert.Equal(t, 1, exit)
				assert.True(t, results[0].IsError)
			} else {
				require.NoError(t, err)
				if tc.failStart {
					assert.Equal(t, 1, exit)
					assert.Equal(t, "provider authentication failed", results[0].ErrorMessage)
					assert.Equal(t, codexSubtypeFailed, results[0].Subtype)
				} else {
					assert.Zero(t, exit)
				}
			}
			if tc.failStart || tc.zeroUsage || tc.zeroStream {
				assert.Zero(t, metrics.InputTokens)
				assert.Zero(t, metrics.OutputTokens)
			} else {
				assert.Equal(t, 20, metrics.InputTokens, "known stream input must survive incomplete evidence")
				assert.Equal(t, 8, metrics.OutputTokens, "known stream output must survive incomplete evidence")
				assert.Equal(t, 2, metrics.ReasoningTokens)
			}
		})
	}
}

func TestCodexCollection_InvalidRootTotalsPreserveStreamUsage(t *testing.T) {
	first := testCodexRootUsage(recordedRoot0158, "model-a", int(^uint(0)>>1), 0, 0)
	second := testCodexRootUsage(recordedRoot0158, "model-b", 1, 0, 0)
	second = strings.NewReplacer("root-turn", "other-turn", "root-response", "other-response").Replace(second)
	fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "", map[string]string{"root-a.jsonl": first, "root-b.jsonl": second})
	metrics := &RunMetrics{Model: "stream-model", InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2}
	err := (CodexRuntime{}).collectCodexUsage(t.Context(), "overflow-root", recordedRoot0158, metrics, nil, true)
	require.ErrorContains(t, err, "overflows")
	assert.Equal(t, 20, metrics.InputTokens)
	assert.Equal(t, 8, metrics.OutputTokens)
	assert.Equal(t, 2, metrics.ReasoningTokens)
	assert.Equal(t, map[string]ModelUsage{"stream-model": {Requests: 1, InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2, CostUnavailable: true}}, metrics.PerModelUsage)
}

func TestCodexCollection_ChildUsageStillRequiresMissingRoot(t *testing.T) {
	body, err := os.ReadFile("testdata/codex/native-subagents/0158/probe-alpha.jsonl")
	require.NoError(t, err)
	fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "", map[string]string{"child.jsonl": string(body)})
	metrics := &RunMetrics{Model: "gpt-5.6-luna"}
	err = (CodexRuntime{}).collectCodexUsage(t.Context(), "child-only", recordedRoot0158, metrics, nil, false)
	require.ErrorContains(t, err, "root has no collected response usage")
	assert.Equal(t, 3, metrics.InputTokens)
	assert.Equal(t, 8, metrics.OutputTokens)
	assert.Equal(t, 9201, metrics.CacheCreationInputTokens, "retain child usage even when the stream had no counters")
}

func TestCodexCollection_RequiresExpectedChildEvidence(t *testing.T) {
	body, err := os.ReadFile("testdata/codex/native-subagents/0158/probe-alpha.jsonl")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body, missing string
		listed, completed   bool
		wantChildUsage      bool
	}{
		{"missing rollout", "", "no collected response usage", false, true, false},
		{"metadata without response", strings.SplitN(string(body), "\n", 2)[0], "no collected response usage", true, true, false},
		{"stale root", strings.ReplaceAll(string(body), recordedRoot0158, "another-root"), "no collected response usage", true, true, false},
		{"unfinished with incurred usage", string(body), "did not deliver a completed result", true, false, true},
		{"complete", string(body), "", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies := map[string]string{"root.jsonl": testCodexRootUsage(recordedRoot0158, "root-model", 20, 10, 2)}
			if tc.listed {
				bodies["child.jsonl"] = tc.body
			}
			fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "", bodies)
			metrics := &RunMetrics{Model: "root-model", InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2}
			err := (CodexRuntime{}).collectCodexUsage(t.Context(), "usage-evidence", recordedRoot0158, metrics, map[string]bool{recordedAlpha0158: tc.completed}, true)
			if tc.missing == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.missing)
				assert.Contains(t, err.Error(), recordedAlpha0158)
			}
			assert.Equal(t, ModelUsage{Requests: 1, InputTokens: 20, OutputTokens: 8, ReasoningTokens: 2, CostUnavailable: true}, metrics.PerModelUsage["root-model"])
			if tc.wantChildUsage {
				assert.Equal(t, 23, metrics.InputTokens, "incomplete results still retain incurred usage")
				assert.Equal(t, 16, metrics.OutputTokens)
				assert.Equal(t, 9201, metrics.CacheCreationInputTokens)
				assert.Equal(t, ModelUsage{Requests: 1, InputTokens: 3, OutputTokens: 8, CacheCreationInputTokens: 9201, CostUnavailable: true}, metrics.PerModelUsage["gpt-5.6-luna"])
			} else {
				assert.Equal(t, 20, metrics.InputTokens)
				assert.NotContains(t, metrics.PerModelUsage, "gpt-5.6-luna")
			}
		})
	}
}

func TestCodexCollection_InterruptedRootRecoversRecordedUsage(t *testing.T) {
	body, err := os.ReadFile("testdata/codex/native-subagents/0158/root.jsonl")
	require.NoError(t, err)
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0", "", CodexRuntime{}.codexSessionsDir()+"/root.jsonl")
	t.Setenv("FULLSEND_TEST_DOWNLOAD_BODY", string(body))
	metrics := &RunMetrics{Model: "gpt-5.6-luna"}
	require.NoError(t, (CodexRuntime{}).collectCodexUsage(t.Context(), "interrupted-root", recordedRoot0158, metrics, nil, true))
	assert.Equal(t, 18, metrics.InputTokens)
	assert.Equal(t, 624, metrics.OutputTokens)
	assert.Equal(t, 170, metrics.ReasoningTokens)
	assert.Equal(t, 78328, metrics.CacheReadInputTokens)
	assert.Equal(t, 17845, metrics.CacheCreationInputTokens)
	assert.Equal(t, 1, metrics.PerModelUsage["gpt-5.6-luna"].Requests)
	assert.Zero(t, metrics.NumTurns, "accounting recovery must not invent successful completion")
}

func TestCodexCollection_LaterInterruptedRootDoesNotLoseUsage(t *testing.T) {
	body, err := os.ReadFile("testdata/codex/native-subagents/0158/root.jsonl")
	require.NoError(t, err)
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0", "", CodexRuntime{}.codexSessionsDir()+"/root.jsonl")
	t.Setenv("FULLSEND_TEST_DOWNLOAD_BODY", string(body))
	metrics := &RunMetrics{Model: "gpt-5.6-luna", NumTurns: 1, InputTokens: 3, OutputTokens: 14}
	require.NoError(t, (CodexRuntime{}).collectCodexUsage(t.Context(), "later-interrupted-root", recordedRoot0158, metrics, nil, true))
	assert.Equal(t, 18, metrics.InputTokens, "persisted later responses replace, rather than add to, an earlier stream snapshot")
	assert.Equal(t, 624, metrics.OutputTokens)
	assert.Equal(t, 170, metrics.ReasoningTokens)
	assert.Equal(t, 78328, metrics.CacheReadInputTokens)
	assert.Equal(t, 17845, metrics.CacheCreationInputTokens)
	assert.Equal(t, 1, metrics.NumTurns, "accounting must not invent completed turns")
}

func TestCodexCollection_RunRequiresChildFinalResult(t *testing.T) {
	body, err := os.ReadFile("testdata/codex/native-subagents/0158/probe-alpha.jsonl")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, state, message, followup string
		wantExit                       int
	}{
		{"no final", "running", "", "", 1},
		{"empty completed result", "completed", "", "", 1},
		{"failed child", "errored", "provider error", "", 1},
		{"root failed with unfinished child", "running", "", "", 1},
		{"completed final", "completed", "Verified the requested result.", "", 0},
		{"unclosed final challenger", "completed", "Verified the requested result.", "", 1},
		{"reopened child must finish again", "completed", "First task is complete.", "send_input", 1},
		// Codex removes a closed thread; later calls report it not_found.
		{"wait after close", "completed", "Verified the requested result.", "wait", 0},
		{"double close", "completed", "Verified the requested result.", "close_agent", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := json.Marshal(map[string]any{recordedAlpha0158: map[string]string{"status": tc.state, "message": tc.message}})
			require.NoError(t, err)
			followup := ""
			closed := ""
			if tc.name != "unclosed final challenger" {
				closed = fmt.Sprintf(`{"type":"item.completed","item":{"id":"close","type":"collab_tool_call","tool":"close_agent","status":"completed","receiver_thread_ids":[%q],"agents_states":%s}}`+"\n", recordedAlpha0158, state)
			}
			switch tc.followup {
			case "send_input":
				followup = fmt.Sprintf(`{"type":"item.completed","item":{"id":"followup","type":"collab_tool_call","tool":"send_input","status":"completed","receiver_thread_ids":[%q],"agents_states":{%q:{"status":"running","message":null}}}}`+"\n", recordedAlpha0158, recordedAlpha0158)
			case "wait", "close_agent":
				followup = fmt.Sprintf(`{"type":"item.completed","item":{"id":"followup","type":"collab_tool_call","tool":%q,"status":"failed","receiver_thread_ids":[%q],"agents_states":{%q:{"status":"not_found","message":null}}}}`+"\n", tc.followup, recordedAlpha0158, recordedAlpha0158)
			}
			stream := fmt.Sprintf(`{"type":"thread.started","thread_id":%q}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"spawn","type":"collab_tool_call","tool":"spawn_agent","status":"completed","receiver_thread_ids":[%q],"agents_states":{}}}
{"type":"item.completed","item":{"id":"wait","type":"collab_tool_call","tool":"wait","status":"completed","receiver_thread_ids":[%q],"agents_states":%s}}
%s%s{"type":"turn.completed","usage":{"input_tokens":20,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":2}}
`, recordedRoot0158, recordedAlpha0158, recordedAlpha0158, state, closed, followup)
			if tc.name == "root failed with unfinished child" {
				stream += "{\"type\":\"turn.failed\",\"error\":{\"message\":\"root provider failed after dispatch\"}}\n"
			}
			fixture := filepath.Join(t.TempDir(), "stream.ndjson")
			require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o600))
			store := t.TempDir()
			seedCodexManifest(t, store, CodexRuntime{}, nil)
			fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), store, fixture, map[string]string{
				"root.jsonl":  testCodexRootUsage(recordedRoot0158, "gpt-5.6-luna", 20, 10, 2),
				"child.jsonl": string(body),
			})
			metrics := &RunMetrics{}
			var results []ResultEvent
			exit, err := (CodexRuntime{}).Run(t.Context(), RunParams{
				SandboxName: "sb", RepoDir: "/sandbox/workspace/repo", Model: "gpt-5.6-luna", Timeout: time.Minute,
				OnEvent: func(event AgentEvent) {
					if result, ok := event.(ResultEvent); ok {
						results = append(results, result)
					}
				},
			}, ui.New(&bytes.Buffer{}), time.Now(), metrics)
			if tc.wantExit == 0 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrIncompleteEvidence)
				if tc.name == "root failed with unfinished child" {
					assert.ErrorContains(t, err, "did not deliver a completed result")
				}
			}
			assert.Equal(t, tc.wantExit, exit, "a successful parent cannot hide an unfinished or unclosed child")
			assert.Equal(t, 23, metrics.InputTokens)
			assert.Equal(t, 16, metrics.OutputTokens)
			assert.Equal(t, 2, metrics.PerModelUsage["gpt-5.6-luna"].Requests)
			require.Len(t, results, 1, "emit one terminal result after child accounting")
			assert.Equal(t, 23, results[0].InputTokens)
			assert.Equal(t, 16, results[0].OutputTokens)
			assert.Equal(t, 2, results[0].ReasoningTokens)
			assert.Equal(t, 9201, results[0].CacheCreationInputTokens)
			assert.True(t, results[0].CostUnavailable, "unreported dollar cost is not a free run")
			assert.Equal(t, tc.wantExit != 0, results[0].IsError, "terminal summary and exit verdict agree")
		})
	}
}

func TestCodexCollection_FailedDispatchCannotAuthenticateChild(t *testing.T) {
	child, err := os.ReadFile("testdata/codex/native-subagents/0158/probe-alpha.jsonl")
	require.NoError(t, err)
	for _, includeChild := range []bool{false, true} {
		t.Run(fmt.Sprintf("child-rollout-%t", includeChild), func(t *testing.T) {
			// A rejected V2-shaped spawn is not a successful child dispatch,
			// even if the failed event includes an ID and a completed status.
			stream := fmt.Sprintf(`{"type":"thread.started","thread_id":%q}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"denied","type":"collab_tool_call","tool":"spawn_agent","status":"failed","receiver_thread_ids":[%q],"agents_states":{%q:{"status":"completed","message":"not authenticated"}}}}
{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":10,"reasoning_output_tokens":2}}
`, recordedRoot0158, recordedAlpha0158, recordedAlpha0158)
			fixture := filepath.Join(t.TempDir(), "stream.ndjson")
			require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o600))
			store := t.TempDir()
			seedCodexManifest(t, store, CodexRuntime{}, nil)
			bodies := map[string]string{"root.jsonl": testCodexRootUsage(recordedRoot0158, "gpt-5.6-luna", 20, 10, 2)}
			if includeChild {
				bodies["child.jsonl"] = string(child)
			}
			fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), store, fixture, bodies)
			metrics := &RunMetrics{}
			exit, err := (CodexRuntime{}).Run(t.Context(), RunParams{
				SandboxName: "sb", RepoDir: "/sandbox/workspace/repo", Model: "gpt-5.6-luna", Timeout: time.Minute,
			}, ui.New(&bytes.Buffer{}), time.Now(), metrics)
			if includeChild {
				require.ErrorIs(t, err, ErrIncompleteEvidence)
				assert.ErrorContains(t, err, "no observed dispatch")
				assert.NotZero(t, exit)
				assert.Equal(t, 23, metrics.InputTokens, "keep incurred child usage even when dispatch evidence is incomplete")
			} else {
				require.NoError(t, err)
				assert.Zero(t, exit, "a root-only run need not delegate")
				assert.Equal(t, 20, metrics.InputTokens)
			}
		})
	}
}

func TestCodexCollection_StreamLossReturnsIncompleteEvidence(t *testing.T) {
	stream := fmt.Sprintf(`{"type":"thread.started","thread_id":%q}
{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":10,"reasoning_output_tokens":2}}
`, recordedRoot0158) + strings.Repeat("x", codexRedactMaxLine+1024) + "\n"
	fixture := filepath.Join(t.TempDir(), "stream.ndjson")
	require.NoError(t, os.WriteFile(fixture, []byte(stream), 0o600))
	store := t.TempDir()
	seedCodexManifest(t, store, CodexRuntime{}, nil)
	fakeOpenshellCodexWithRollouts(t, filepath.Join(t.TempDir(), "log"), store, fixture, map[string]string{
		"root.jsonl": testCodexRootUsage(recordedRoot0158, "gpt-5.6-luna", 20, 10, 2),
	})
	metrics := &RunMetrics{}
	var result ResultEvent
	exit, err := (CodexRuntime{}).Run(t.Context(), RunParams{
		SandboxName: "sb", RepoDir: "/sandbox/workspace/repo", Model: "gpt-5.6-luna", Timeout: time.Minute,
		OnEvent: func(event AgentEvent) {
			if final, ok := event.(ResultEvent); ok {
				result = final
			}
		},
	}, ui.New(&bytes.Buffer{}), time.Now(), metrics)
	require.ErrorIs(t, err, ErrIncompleteEvidence)
	assert.NotZero(t, exit)
	assert.True(t, result.IsError, "an earlier completed turn cannot certify dropped later records")
	assert.Equal(t, 20, metrics.InputTokens)
	assert.Equal(t, 8, metrics.OutputTokens)
}

func TestCodexCollection_ListingRejectsUnboundedAndFailedDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name          string
		count, length int
		fail, cancel  bool
		wantError     bool
	}{
		{"at file limit", 128, 0, false, false, false},
		{"over file limit", 129, 0, false, false, true},
		{"over byte limit", 1, 65536, false, false, true},
		{"remote enumeration failed", 1, 0, true, false, true},
		{"cancelled collection", 1, 0, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := make([]string, tc.count)
			for i := range paths {
				paths[i] = fmt.Sprintf("%s/%s%d.jsonl", CodexRuntime{}.codexSessionsDir(), strings.Repeat("x", tc.length), i)
			}
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0", "", strings.Join(paths, "\n"))
			if tc.fail {
				t.Setenv("FULLSEND_TEST_FAIL_MATCH", "fullsend-codex-list")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			got, err := (CodexRuntime{}).listCodexRollouts(ctx, "bounded-discovery")
			if tc.wantError {
				require.Error(t, err)
				assert.Empty(t, got, "an incomplete listing must never look complete")
			} else {
				require.NoError(t, err)
				assert.Equal(t, paths, got)
			}
		})
	}
}

func TestCodexCollection_EmbeddedListingBoundsAndSymlinks(t *testing.T) {
	python := pythonWithTomllib(t)
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.jsonl"), []byte("private"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "linked-directory")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "outside.jsonl"), filepath.Join(root, "linked-file.jsonl")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "normal.jsonl"), nil, 0o600))
	out, err := exec.Command(python, "-I", "-c", codexListRolloutsPy, root).CombinedOutput()
	require.NoError(t, err, "%s", out)
	var listed []string
	require.NoError(t, json.Unmarshal(out, &listed))
	assert.Equal(t, []string{filepath.Join(root, "normal.jsonl")}, listed)
	for i := 0; i < 128; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("extra-%03d.jsonl", i)), nil, 0o600))
	}
	cmd := exec.Command(python, "-I", "-c", codexListRolloutsPy, root)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	require.Error(t, err)
	assert.Empty(t, stdout.String(), "overflow must not publish a partial JSON listing")
	link := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.Symlink(outside, link))
	require.Error(t, exec.Command(python, "-I", "-c", codexListRolloutsPy, link).Run())
}

func TestCodexCollection_EmbeddedListingRejectsUnreadableSubdirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can enumerate mode-000 directories")
	}
	root := t.TempDir()
	child := filepath.Join(root, "unreadable")
	require.NoError(t, os.Mkdir(child, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(child, "child.jsonl"), nil, 0o600))
	require.NoError(t, os.Chmod(child, 0))
	t.Cleanup(func() { _ = os.Chmod(child, 0o700) })
	cmd := exec.Command(pythonWithTomllib(t), "-I", "-c", codexListRolloutsPy, root)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	require.Error(t, err, "inaccessible child evidence must not become a successful empty listing")
	assert.Empty(t, stdout.String())
}

func TestCodexCollection_PartialExtractionReturnsErrorAndKeepsSafeArtifacts(t *testing.T) {
	for _, tc := range []struct{ name, badName, fail string }{
		{"failed transfer", "unavailable.jsonl", "unavailable.jsonl"},
		{"invalid rollout", "planted.jsonl", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := CodexRuntime{}.codexSessionsDir()
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0", "", root+"/good.jsonl\n"+root+"/"+tc.badName)
			t.Setenv("FULLSEND_TEST_DOWNLOAD_BODY", `{"type":"session_meta","payload":{"id":"root"}}`)
			t.Setenv("FULLSEND_TEST_FAIL_MATCH", tc.fail)
			out := t.TempDir()
			err := (CodexRuntime{}).ExtractTranscripts("partial-extraction", "review", out)
			require.Error(t, err, "missing one transcript must surface to the artifact caller")
			entries, err := os.ReadDir(out)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, "review-good.jsonl", entries[0].Name())
			assert.JSONEq(t, `{"type":"session_meta","payload":{"id":"root"}}`, strings.TrimSpace(readFileString(t, filepath.Join(out, entries[0].Name()))))
		})
	}
}

func TestCodexCollection_PublishFailureKeepsPreviousArtifact(t *testing.T) {
	out := t.TempDir()
	root, err := os.OpenRoot(out)
	require.NoError(t, err)
	defer root.Close()
	previous := filepath.Join(out, "child.jsonl")
	require.NoError(t, os.WriteFile(previous, []byte("previous redacted artifact"), 0o600))
	err = codexPublishTranscript(root, filepath.Join(t.TempDir(), "missing.jsonl"), "child.jsonl")
	require.Error(t, err)
	assert.Equal(t, "previous redacted artifact", readFileString(t, previous))
	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	require.Len(t, entries, 1, "failed publication leaves no staging artifact")
}

func TestCodexCollection_CancellationStopsRemainingTransfers(t *testing.T) {
	listed := CodexRuntime{}.codexSessionsDir() + "/first.jsonl\n" + CodexRuntime{}.codexSessionsDir() + "/second.jsonl"
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.158.0", "", listed)
	fake, err := exec.LookPath("openshell")
	require.NoError(t, err)
	require.NoError(t, os.Rename(fake, fake+"-base"))
	started := filepath.Join(t.TempDir(), "transfer-started")
	wrapper := "#!/bin/sh\ncase \"$*\" in *fullsend-codex-rollout*) touch " + shellQuote(started) + "; exec sleep 30;; esac\nexec " + shellQuote(fake+"-base") + " \"$@\"\n"
	require.NoError(t, os.WriteFile(fake, []byte(wrapper), 0o755))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	private := t.TempDir()
	finished := make(chan error, 1)
	go func() {
		_, err := (CodexRuntime{}).downloadCodexRollouts(ctx, "cancel-transfer", private)
		finished <- err
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil }, 10*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled, "stop before scheduling another transfer")
	case <-time.After(10 * time.Second):
		t.Fatal("collection did not stop after cancellation")
	}
	entries, err := os.ReadDir(private)
	require.NoError(t, err)
	assert.Empty(t, entries, "interrupted transfers leave no raw partial file")
}
