package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexFixturePath(name string) string {
	return filepath.Join("testdata", "codex", name)
}

func readCodexFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(codexFixturePath(name))
	require.NoError(t, err)
	return data
}

// collectCodexEvents parses a fixture and returns every emitted event plus the
// thread id. parseCodexStream must not error on any fixture: a malformed or
// truncated line is data, not a read failure.
func collectCodexEvents(t *testing.T, name string) ([]AgentEvent, string) {
	t.Helper()
	f, err := os.Open(codexFixturePath(name))
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })

	var events []AgentEvent
	threadID, err := parseCodexStream(f, func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)
	return events, threadID
}

// codexEventsOfType filters the collected events down to one concrete type.
func codexEventsOfType[T AgentEvent](events []AgentEvent) []T {
	var out []T
	for _, evt := range events {
		if e, ok := evt.(T); ok {
			out = append(out, e)
		}
	}
	return out
}

func codexOnlyResult(t *testing.T, events []AgentEvent) ResultEvent {
	t.Helper()
	results := codexEventsOfType[ResultEvent](events)
	require.Len(t, results, 1, "exactly one ResultEvent per stream")
	return results[0]
}

// --- the live capture -------------------------------------------------------

func TestParseCodexStream_BasicRun(t *testing.T) {
	t.Parallel()

	events, threadID := collectCodexEvents(t, "basic_run.jsonl")

	assert.Equal(t, "01a062d8-3c06-78f1-95f2-0fe3e261d47f", threadID)

	// No InitEvent: the stream carries neither a model nor a CLI version.
	assert.Empty(t, codexEventsOfType[InitEvent](events),
		"codex exec --json has no model or version on the wire")

	thinking := codexEventsOfType[ThinkingEvent](events)
	require.Len(t, thinking, 1)
	assert.Contains(t, thinking[0].Text, "Executing commands")

	texts := codexEventsOfType[TextEvent](events)
	require.Len(t, texts, 2)
	assert.Contains(t, texts[0].Text, "list the workspace")
	assert.Equal(t, "done", texts[1].Text)

	// The capture has item.started *and* item.completed for both the command
	// and the file change; only the completions are reported.
	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 2)
	assert.Equal(t, "Bash", tools[0].Name)
	assert.Equal(t, "$ /bin/zsh -lc 'ls .'", tools[0].Summary,
		"a successful command shows the command, never its output")
	assert.Equal(t, "Write", tools[1].Name, "kind=add maps to Write")
	assert.Equal(t, "/sandbox/workspace/repo/hello.txt", tools[1].Summary)

	tokens := codexEventsOfType[TokensEvent](events)
	require.Len(t, tokens, 1)
	assert.Equal(t, 41320, tokens[0].InputTokens)
	assert.Equal(t, 295, tokens[0].OutputTokens)
	assert.Equal(t, 74, tokens[0].ReasoningTokens)
	assert.Equal(t, 27386, tokens[0].CacheRead)
	assert.Equal(t, 13925, tokens[0].CacheWrite)

	result := codexOnlyResult(t, events)
	assert.False(t, result.IsError)
	assert.Empty(t, result.Subtype)
	assert.Equal(t, 1, result.NumTurns)
	assert.Zero(t, result.TotalCostUSD, "codex reports no cost")
	assert.Equal(t, 41320, result.InputTokens)
	assert.Equal(t, 295, result.OutputTokens)
	assert.Equal(t, 74, result.ReasoningTokens)
	assert.Equal(t, 13925, result.CacheCreationInputTokens)
	assert.Equal(t, 27386, result.CacheReadInputTokens)
}

// --- fixture table ----------------------------------------------------------

func TestParseCodexStream_Fixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fixture     string
		threadID    string
		wantErr     bool
		subtype     string
		errContains string
		numTurns    int
		toolNames   []string
	}{
		{
			name:        "turn.failed is a failed run",
			fixture:     "turn_failed.jsonl",
			threadID:    "01a06300-0000-7000-8000-000000000001",
			wantErr:     true,
			subtype:     codexSubtypeFailed,
			errContains: "exceeded retry limit",
			numTurns:    0,
			toolNames:   []string{"Bash"},
		},
		{
			name:      "warnings and a top-level error before a completed turn are not failures",
			fixture:   "error_event.jsonl",
			threadID:  "01a06300-0000-7000-8000-000000000002",
			wantErr:   false,
			numTurns:  1,
			toolNames: nil,
		},
		{
			name:        "a top-level error with no terminal event is incomplete",
			fixture:     "critical_error_only.jsonl",
			threadID:    "01a06300-0000-7000-8000-000000000003",
			wantErr:     true,
			subtype:     codexSubtypeIncomplete,
			errContains: "401 Unauthorized",
			numTurns:    0,
		},
		{
			name:     "every tool item kind",
			fixture:  "mcp_and_file_change.jsonl",
			threadID: "01a06300-0000-7000-8000-000000000004",
			numTurns: 1,
			toolNames: []string{
				"Write", "Edit", "Edit", // add, update, delete
				"Edit",                   // the failed patch
				"mcp__github__get_issue", // succeeded
				"mcp__jira__search",      // errored
				"WebSearch",
				"Agent",
				"Bash", // declined
				"Bash", // completed
			},
		},
		{
			name:      "malformed and empty lines are skipped",
			fixture:   "malformed_line.jsonl",
			threadID:  "01a06300-0000-7000-8000-000000000006",
			numTurns:  1,
			toolNames: []string{"Bash"},
		},
		{
			name:      "a truncated stream yields what it has",
			fixture:   "truncated.jsonl",
			threadID:  "01a06300-0000-7000-8000-000000000007",
			wantErr:   true,
			subtype:   codexSubtypeIncomplete,
			numTurns:  0,
			toolNames: []string{"Bash"},
		},
		{
			name:      "unknown top-level and item types are skipped",
			fixture:   "unknown_types.jsonl",
			threadID:  "01a06300-0000-7000-8000-000000000005",
			numTurns:  1,
			toolNames: []string{"Bash"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, threadID := collectCodexEvents(t, tt.fixture)
			assert.Equal(t, tt.threadID, threadID)

			var names []string
			for _, tool := range codexEventsOfType[ToolUseEvent](events) {
				names = append(names, tool.Name)
			}
			assert.Equal(t, tt.toolNames, names)

			result := codexOnlyResult(t, events)
			assert.Equal(t, tt.wantErr, result.IsError)
			assert.Equal(t, tt.subtype, result.Subtype)
			assert.Equal(t, tt.numTurns, result.NumTurns)
			if tt.errContains != "" {
				assert.Contains(t, result.ErrorMessage, tt.errContains)
			}
		})
	}
}

// --- per-item mapping -------------------------------------------------------

func TestParseCodexStream_ToolSummaries(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "mcp_and_file_change.jsonl")
	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 10)

	assert.Equal(t, "/sandbox/workspace/repo/new.go", tools[0].Summary)
	assert.Equal(t, "/sandbox/workspace/repo/main.go", tools[1].Summary)
	assert.Equal(t, "/sandbox/workspace/repo/old.go", tools[2].Summary,
		"a delete is still an Edit of an existing path")
	assert.Equal(t, "/sandbox/workspace/repo/locked.go (failed)", tools[3].Summary)

	assert.Empty(t, tools[4].Summary, "a successful MCP call surfaces no payload")
	assert.Equal(t, "server not reachable", tools[5].Summary)

	assert.Equal(t, "codex exec json event schema", tools[6].Summary)
	assert.Equal(t, "spawn_agent (2 agent(s))", tools[7].Summary)

	assert.Equal(t, "$ rm -rf / (blocked)", tools[8].Summary,
		"a declined command was refused before it ran, not a failure")
	assert.Equal(t, "$ go test ./... -run TestCodex", tools[9].Summary,
		"multi-line commands are collapsed and successful output is not shown")

	// The reasoning item became a ThinkingEvent, the todo_list nothing.
	thinking := codexEventsOfType[ThinkingEvent](events)
	require.Len(t, thinking, 1)
	assert.Contains(t, thinking[0].Text, "Planning")
}

func TestParseCodexStream_FailedCommandShowsOutputTail(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "turn_failed.jsonl")
	tools := codexEventsOfType[ToolUseEvent](events)
	require.Len(t, tools, 1)
	assert.Equal(t, "Bash", tools[0].Name)
	assert.Contains(t, tools[0].Summary, "$ make build")
	assert.Contains(t, tools[0].Summary, "exit 1")
	assert.Contains(t, tools[0].Summary, "go: build failed",
		"a failed command surfaces its output so the failure is diagnosable")
}

func TestParseCodexStream_ErrorItemsAreNotFatal(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "error_event.jsonl")

	errs := codexEventsOfType[ErrorEvent](events)
	require.Len(t, errs, 2, "both the error item and the top-level error are shown")
	assert.Equal(t, "warning", errs[0].ErrorType)
	assert.Contains(t, errs[0].Message, "model rerouted")
	assert.Equal(t, "error", errs[1].ErrorType)
	assert.Contains(t, errs[1].Message, "stream disconnected")

	result := codexOnlyResult(t, events)
	assert.False(t, result.IsError, "turn.completed decides the verdict, not the errors before it")
	assert.Empty(t, result.ErrorMessage)
}

func TestParseCodexStream_UsageIsCumulativeNotSummed(t *testing.T) {
	t.Parallel()

	// turn.completed carries the thread's running total (usage_from_last_total
	// in event_processor_with_jsonl_output.rs), so two turns must report the
	// second value, not their sum — while each TokensEvent shows the delta.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":10,"cache_write_input_tokens":5,"output_tokens":20,"reasoning_output_tokens":2}}`,
		`{"type":"turn.completed","usage":{"input_tokens":250,"cached_input_tokens":40,"cache_write_input_tokens":5,"output_tokens":70,"reasoning_output_tokens":9}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	tokens := codexEventsOfType[TokensEvent](events)
	require.Len(t, tokens, 2)
	assert.Equal(t, 100, tokens[0].InputTokens)
	assert.Equal(t, 150, tokens[1].InputTokens, "second turn reports its delta")
	assert.Equal(t, 50, tokens[1].OutputTokens)
	assert.Equal(t, 30, tokens[1].CacheRead)
	assert.Equal(t, 0, tokens[1].CacheWrite)

	result := codexOnlyResult(t, events)
	assert.Equal(t, 2, result.NumTurns)
	assert.Equal(t, 250, result.InputTokens, "cumulative, not 350")
	assert.Equal(t, 70, result.OutputTokens)
	assert.Equal(t, 9, result.ReasoningTokens)
	assert.Equal(t, 5, result.CacheCreationInputTokens)
	assert.Equal(t, 40, result.CacheReadInputTokens)
}

func TestParseCodexStream_TurnFailedFallsBackToCriticalError(t *testing.T) {
	t.Parallel()

	// The processor reuses last_critical_error when the failed turn carries no
	// error of its own; an empty message on the wire must not lose the reason.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"error","message":"upstream connect error"}`,
		`{"type":"turn.failed","error":{"message":""}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	result := codexOnlyResult(t, events)
	assert.True(t, result.IsError)
	assert.Equal(t, codexSubtypeFailed, result.Subtype)
	assert.Equal(t, "upstream connect error", result.ErrorMessage)
}

func TestParseCodexStream_NoTerminalEventIsIncomplete(t *testing.T) {
	t.Parallel()

	// An interrupted turn emits neither turn.completed nor turn.failed, and
	// codex exec can still exit 0, so the absence of a terminal event has to
	// fail the run on its own.
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"working"}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	result := codexOnlyResult(t, events)
	assert.True(t, result.IsError)
	assert.Equal(t, codexSubtypeIncomplete, result.Subtype)
	assert.Empty(t, result.ErrorMessage, "nothing explained the truncation")
}

func TestParseCodexStream_ReadErrorStillEmitsResult(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	reader := iotest.ErrReader(wantErr)

	var events []AgentEvent
	threadID, err := parseCodexStream(reader, func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.ErrorIs(t, err, wantErr)
	assert.Empty(t, threadID)

	result := codexOnlyResult(t, events)
	assert.True(t, result.IsError, "a lost stream is not a successful run")
	assert.Equal(t, codexSubtypeIncomplete, result.Subtype)
}

func TestParseCodexStream_OverlongLineIsSkipped(t *testing.T) {
	t.Parallel()

	huge := `{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"` +
		strings.Repeat("x", streamBufSize+1024) + `"}}`
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		huge,
		`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"after"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	texts := codexEventsOfType[TextEvent](events)
	require.Len(t, texts, 1, "the oversized line is dropped, the stream continues")
	assert.Equal(t, "after", texts[0].Text)
	assert.False(t, codexOnlyResult(t, events).IsError)
}

// --- redaction --------------------------------------------------------------

func TestCodexSummaries_RedactSecrets(t *testing.T) {
	t.Parallel()

	// A token in the command, in a failed command's output, and in a search
	// query must not reach the renderer or the CI annotations.
	token := "ghp_" + strings.Repeat("a", 36)
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.completed","item":{"id":"i0","type":"command_execution","command":"curl -H 'Authorization: Bearer ` + token +
			`' https://api.example","aggregated_output":"denied for ` + token + `","exit_code":22,"status":"failed"}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"web_search","query":"` + token + `","action":{"type":"search"}}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`,
	}, "\n")

	var events []AgentEvent
	_, err := parseCodexStream(strings.NewReader(stream), func(evt AgentEvent) {
		events = append(events, evt)
	})
	require.NoError(t, err)

	for _, tool := range codexEventsOfType[ToolUseEvent](events) {
		assert.NotContains(t, tool.Summary, token, "tool summary leaked a secret: %s", tool.Name)
	}
}

func TestCodexOutputTail(t *testing.T) {
	t.Parallel()

	assert.Empty(t, codexOutputTail(""))
	assert.Equal(t, "hello", codexOutputTail("  hello\n"))

	long := strings.Repeat("a", codexOutputTailMax) + "TAIL"
	got := codexOutputTail(long)
	assert.True(t, strings.HasPrefix(got, "..."), "the head is dropped, not the tail")
	assert.True(t, strings.HasSuffix(got, "TAIL"))

	assert.Equal(t, "(output too large to summarize)",
		codexOutputTail(strings.Repeat("z", codexOutputScanMax+1)),
		"an unbounded output is dropped rather than cut before redaction")
}

func TestCodexMcpToolName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "mcp__github__get_issue", codexMcpToolName("github", "get_issue"))
	assert.Equal(t, "mcp__github", codexMcpToolName("github", ""))
	assert.Equal(t, "mcp__search", codexMcpToolName("", "search"))
	assert.Equal(t, "mcp", codexMcpToolName("", ""))
}

func TestCodexFileChangeTool(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Write", codexFileChangeTool("add"))
	assert.Equal(t, "Edit", codexFileChangeTool("update"))
	assert.Equal(t, "Edit", codexFileChangeTool("delete"))
	assert.Equal(t, "Edit", codexFileChangeTool("rename_someday"))
}

// --- metrics ----------------------------------------------------------------

func TestApplyCodexMetrics(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "basic_run.jsonl")

	var metrics RunMetrics
	for _, evt := range events {
		applyCodexMetrics(&metrics, evt)
	}

	assert.Equal(t, 1, metrics.NumTurns)
	assert.Equal(t, 41320, metrics.InputTokens)
	assert.Equal(t, 295, metrics.OutputTokens)
	assert.Equal(t, 74, metrics.ReasoningTokens)
	assert.Equal(t, 13925, metrics.CacheCreationInputTokens)
	assert.Equal(t, 27386, metrics.CacheReadInputTokens)
	assert.Equal(t, int32(2), metrics.ToolCalls.Load())

	// Left for the runner: the stream carries neither.
	assert.Zero(t, metrics.TotalCostUSD)
	assert.Empty(t, metrics.Model)

	assert.NotPanics(t, func() { applyCodexMetrics(nil, ResultEvent{}) })
}

func TestApplyCodexMetrics_CountsEveryFileChange(t *testing.T) {
	t.Parallel()

	events, _ := collectCodexEvents(t, "mcp_and_file_change.jsonl")

	var metrics RunMetrics
	for _, evt := range events {
		applyCodexMetrics(&metrics, evt)
	}
	// One tool call per change in a file_change item, plus the MCP, search,
	// collab and command items.
	assert.Equal(t, int32(10), metrics.ToolCalls.Load())
}

// --- capture detection and verdict ------------------------------------------

func TestIsCodexStreamCapture(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"basic_run.jsonl", "turn_failed.jsonl", "error_event.jsonl",
		"critical_error_only.jsonl", "mcp_and_file_change.jsonl",
		"malformed_line.jsonl", "truncated.jsonl", "unknown_types.jsonl",
	} {
		assert.True(t, isCodexStreamCapture(readCodexFixture(t, name)), "fixture %s", name)
	}

	assert.False(t, isCodexStreamCapture(nil))
	assert.False(t, isCodexStreamCapture([]byte(`{"type":"session","id":"ses_1","version":3}`)),
		"a pi capture is not a codex capture")

	// codex's own rollout session files use underscored inner names inside
	// session_meta/response_item/event_msg envelopes, so they must not match.
	rollout := `{"timestamp":"2026-09-02T11:58:53Z","type":"session_meta","payload":{"id":"t1"}}` + "\n" +
		`{"timestamp":"2026-09-02T11:58:54Z","type":"event_msg","payload":{"type":"item_completed"}}`
	assert.False(t, isCodexStreamCapture([]byte(rollout)),
		"rollout transcripts must not be mistaken for a --json capture")

	// Tool output quoting a marker cannot match: the quotes are escaped inside
	// a JSON string.
	quoted := `{"type":"item.updated","item":{"id":"i0","type":"agent_message","text":"the type is \"turn.completed\""}}`
	assert.False(t, isCodexStreamCapture([]byte(quoted)))

	// A hand-reformatted capture with spaces after the colon still matches.
	assert.True(t, isCodexStreamCapture([]byte(`{"type": "turn.completed", "usage": {}}`)))
}

func TestCodexStreamVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fixture     string
		wantErr     bool
		subtype     string
		errContains string
	}{
		{fixture: "basic_run.jsonl"},
		{fixture: "error_event.jsonl"},
		{fixture: "mcp_and_file_change.jsonl"},
		{fixture: "malformed_line.jsonl"},
		{fixture: "unknown_types.jsonl"},
		{fixture: "turn_failed.jsonl", wantErr: true, subtype: codexSubtypeFailed, errContains: "429"},
		{
			fixture: "critical_error_only.jsonl", wantErr: true,
			subtype: codexSubtypeIncomplete, errContains: "401 Unauthorized",
		},
		{fixture: "truncated.jsonl", wantErr: true, subtype: codexSubtypeIncomplete},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()

			te, ok := codexStreamVerdict(readCodexFixture(t, tt.fixture), tt.fixture)
			require.True(t, ok)
			assert.Equal(t, tt.fixture, te.Source)
			assert.Equal(t, tt.wantErr, te.IsError)
			assert.Equal(t, tt.subtype, te.Subtype)
			if tt.errContains != "" {
				assert.Contains(t, te.ErrorMessage, tt.errContains)
			}

			// The path form must agree with the bytes form.
			fromPath, okPath := parseCodexTranscriptFile(codexFixturePath(tt.fixture))
			require.True(t, okPath)
			assert.Equal(t, te, fromPath)
		})
	}
}

func TestCodexStreamVerdict_RejectsNonCaptures(t *testing.T) {
	t.Parallel()

	_, ok := codexStreamVerdict([]byte("not a capture\n"), "x.jsonl")
	assert.False(t, ok)

	_, ok = parseCodexTranscriptFile(filepath.Join("testdata", "codex", "does-not-exist.jsonl"))
	assert.False(t, ok, "a missing file is not a verdict")
}

func TestCodexStreamVerdict_BoundsTheErrorMessage(t *testing.T) {
	t.Parallel()

	// piSummarize caps the message inside the parser and truncateError caps it
	// again on the way out; a runaway error must not flood the annotation.
	long := strings.Repeat("e", maxTranscriptErrorLength*2)
	stream := `{"type":"thread.started","thread_id":"t1"}` + "\n" +
		`{"type":"turn.failed","error":{"message":"` + long + `"}}`

	te, ok := codexStreamVerdict([]byte(stream), "output.jsonl")
	require.True(t, ok)
	assert.True(t, te.IsError)
	assert.LessOrEqual(t, len(te.ErrorMessage), piSummaryMax)
}
