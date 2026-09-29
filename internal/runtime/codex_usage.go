package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"
)

// Codex 0.157.0 and 0.158.0 persist one token_usage_record for each
// completed response whose provider reported usage. Its usage is a delta;
// the accompanying turn/thread counters may include inherited history.
// The reduced live fixtures in testdata/codex/native-subagents pin this wire.
type codexRolloutMeta struct {
	ThreadID       string `json:"id"`
	SessionID      string `json:"session_id"`
	ParentThreadID string `json:"parent_thread_id"`
	Role           string `json:"agent_role"`
}

type codexResponseUsageRecord struct {
	ThreadID   string      `json:"thread_id"`
	SessionID  string      `json:"session_id"`
	TurnID     string      `json:"turn_id"`
	RootTurnID string      `json:"root_turn_id"`
	ResponseID string      `json:"response_id"`
	Usage      *codexUsage `json:"usage"`
}

type codexUsageKey struct{ ThreadID, ResponseID string }

type codexAttributedUsage struct {
	Record codexResponseUsageRecord
	Model  string
}

type codexRolloutUsage struct {
	Meta      codexRolloutMeta
	Responses []codexAttributedUsage
}

// parseCodexRolloutUsage reads a complete bounded rollout without retaining
// prompt/tool content. Models are joined by turn ID, not the last context in
// the file. Missing model metadata remains visible as an unknown bucket.
func parseCodexRolloutUsage(r io.Reader) (codexRolloutUsage, error) {
	var out codexRolloutUsage
	limited := &io.LimitedReader{R: r, N: codexMaxArtifactBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64*1024), codexMaxArtifactBytes+1)
	models := make(map[string]string)
	sawMeta := false
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return out, fmt.Errorf("codex rollout line %d is not JSON", lineNumber)
		}
		if !codexRolloutEnvelopes[envelope.Type] {
			return out, fmt.Errorf("codex rollout line %d has an unknown envelope", lineNumber)
		}
		switch envelope.Type {
		case "session_meta":
			if sawMeta {
				return out, fmt.Errorf("codex rollout has multiple session identities")
			}
			if err := json.Unmarshal(envelope.Payload, &out.Meta); err != nil || out.Meta.ThreadID == "" || out.Meta.SessionID == "" {
				return out, fmt.Errorf("codex rollout has invalid session identity")
			}
			sawMeta = true
		case "turn_context":
			var turn struct {
				TurnID string `json:"turn_id"`
				Model  string `json:"model"`
			}
			if err := json.Unmarshal(envelope.Payload, &turn); err != nil {
				return out, fmt.Errorf("codex rollout line %d has invalid turn context", lineNumber)
			}
			if turn.TurnID == "" {
				continue
			}
			model := strings.TrimSpace(strings.TrimPrefix(turn.Model, "openai/"))
			if previous, ok := models[turn.TurnID]; ok && previous != model {
				return out, fmt.Errorf("codex rollout has conflicting models for a turn")
			}
			models[turn.TurnID] = model
		case "token_usage_record":
			var rec codexResponseUsageRecord
			var presence struct {
				Usage map[string]json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(envelope.Payload, &rec) != nil || json.Unmarshal(envelope.Payload, &presence) != nil {
				return out, fmt.Errorf("codex rollout line %d has invalid usage", lineNumber)
			}
			// Only cache_write_input_tokens is optional in upstream TokenUsage.
			// Missing/null required fields are not a reported zero-token response.
			for _, key := range []string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens"} {
				value := bytes.TrimSpace(presence.Usage[key])
				if len(value) == 0 || bytes.Equal(value, []byte("null")) {
					return out, fmt.Errorf("codex rollout line %d has incomplete usage", lineNumber)
				}
			}
			if rec.ThreadID == "" || rec.SessionID == "" || rec.TurnID == "" || rec.RootTurnID == "" || rec.ResponseID == "" {
				return out, fmt.Errorf("codex rollout line %d has incomplete response identity", lineNumber)
			}
			if err := validateCodexResponseUsage(rec.Usage); err != nil {
				return out, fmt.Errorf("codex rollout line %d: %w", lineNumber, err)
			}
			out.Responses = append(out.Responses, codexAttributedUsage{Record: rec})
		}
	}
	if limited.N == 0 {
		return out, fmt.Errorf("codex rollout exceeds %d-byte artifact limit", codexMaxArtifactBytes)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("reading complete codex rollout: %w", err)
	}
	if !sawMeta {
		return out, fmt.Errorf("codex rollout is missing session identity")
	}
	for i := range out.Responses {
		model := models[out.Responses[i].Record.TurnID]
		if model == "" {
			model = "unknown"
		}
		out.Responses[i].Model = model
	}
	return out, nil
}

func validateCodexResponseUsage(u *codexUsage) error {
	if u == nil {
		return fmt.Errorf("missing response usage")
	}
	if u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedInputTokens < 0 || u.CacheWriteInputTokens < 0 || u.ReasoningOutputTokens < 0 {
		return fmt.Errorf("negative response usage")
	}
	// Subtract only after bounding each subset; adding two hostile counters
	// first could overflow and accidentally pass the subset check.
	if u.CachedInputTokens > u.InputTokens || u.CacheWriteInputTokens > u.InputTokens-u.CachedInputTokens || u.ReasoningOutputTokens > u.OutputTokens {
		return fmt.Errorf("response usage subsets exceed their totals")
	}
	return nil
}

// foldCodexChildUsage produces child-only contributions for one depth-one
// run. Deduplication spans files and repeated records; role names are never
// identities. A conflicting duplicate fails rather than depending on the
// order the sandbox happened to enumerate files.
func foldCodexChildUsage(rootID string, rollouts []codexRolloutUsage) (map[string]ModelUsage, error) {
	return foldCodexThreadUsage(rootID, rollouts, false)
}

// foldCodexRootUsage recovers observed parent usage when interruption left
// the exec stream without a completed-turn snapshot. The caller must replace
// that missing parent contribution, never add this to a successful snapshot.
func foldCodexRootUsage(rootID string, rollouts []codexRolloutUsage) (map[string]ModelUsage, error) {
	return foldCodexThreadUsage(rootID, rollouts, true)
}

// foldCodexThreadUsage takes rollouts from parseCodexRolloutUsage, the trust
// boundary that guarantees complete identities, validated non-nil usage and a
// non-empty model.
func foldCodexThreadUsage(rootID string, rollouts []codexRolloutUsage, rootOnly bool) (map[string]ModelUsage, error) {
	if rootID == "" {
		return nil, fmt.Errorf("codex usage requires the root thread ID")
	}
	byModel := make(map[string]ModelUsage)
	seen := make(map[codexUsageKey]codexAttributedUsage)
	invocations := make(map[[2]string]bool)
	for _, rollout := range rollouts {
		meta := rollout.Meta
		matches := meta.ThreadID != "" && meta.ThreadID != rootID && meta.ParentThreadID == rootID && meta.SessionID == rootID
		if rootOnly {
			matches = meta.ThreadID == rootID && meta.ParentThreadID == "" && meta.SessionID == rootID
		}
		if !matches {
			continue
		}
		for _, attributed := range rollout.Responses {
			rec, model := attributed.Record, attributed.Model
			// Forked history can contain parent response records. Their usage
			// belongs to that other thread, not to the enclosing child.
			if rec.ThreadID != meta.ThreadID {
				continue
			}
			if rec.SessionID != rootID {
				return nil, fmt.Errorf("codex response session contradicts its thread identity")
			}
			key := codexUsageKey{rec.ThreadID, rec.ResponseID}
			if previous, ok := seen[key]; ok {
				if previous.Model != model || previous.Record.SessionID != rec.SessionID || previous.Record.TurnID != rec.TurnID || previous.Record.RootTurnID != rec.RootTurnID || *previous.Record.Usage != *rec.Usage {
					return nil, fmt.Errorf("codex usage has conflicting duplicate response records")
				}
				continue
			}
			seen[key] = attributed
			c := rec.Usage.counters()
			u := ModelUsage{InputTokens: c.Input, OutputTokens: c.Output,
				ReasoningTokens: c.Reasoning, CacheReadInputTokens: c.CacheRead,
				CacheCreationInputTokens: c.CacheWrite, CostUnavailable: true}
			invocation := [2]string{rec.ThreadID, model}
			if !invocations[invocation] {
				u.Requests = 1
				invocations[invocation] = true
			}
			total, err := sumCodexModelUsage(byModel[model], u)
			if err != nil {
				return nil, err
			}
			byModel[model] = total
		}
	}
	return byModel, nil
}

// sumCodexModelUsage checks counters originating in agent-writable rollouts
// before adding them. Costs stay unreported for Codex.
func sumCodexModelUsage(a, b ModelUsage) (ModelUsage, error) {
	out := a
	out.CostUnavailable = a.CostUnavailable || b.CostUnavailable
	for _, pair := range []struct {
		dst *int
		src int
	}{
		{&out.Requests, b.Requests}, {&out.InputTokens, b.InputTokens},
		{&out.OutputTokens, b.OutputTokens}, {&out.ReasoningTokens, b.ReasoningTokens},
		{&out.CacheReadInputTokens, b.CacheReadInputTokens},
		{&out.CacheCreationInputTokens, b.CacheCreationInputTokens},
	} {
		if *pair.dst < 0 || pair.src < 0 || pair.src > int(^uint(0)>>1)-*pair.dst {
			return ModelUsage{}, fmt.Errorf("codex usage counter is negative or overflows")
		}
		*pair.dst += pair.src
	}
	return out, nil
}

// addCodexUsage applies a child-only fold once, after the root stream has
// stopped updating metrics. Validate all additions before changing anything.
// The caller seeds the parent's PerModelUsage entry before calling this.
func addCodexUsage(m *RunMetrics, byModel map[string]ModelUsage) error {
	if m == nil {
		return fmt.Errorf("codex usage requires run metrics")
	}
	total := ModelUsage{InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
		ReasoningTokens: m.ReasoningTokens, CacheReadInputTokens: m.CacheReadInputTokens,
		CacheCreationInputTokens: m.CacheCreationInputTokens, CostUnavailable: m.CostUnavailable}
	perModel := maps.Clone(m.PerModelUsage)
	if perModel == nil {
		perModel = make(map[string]ModelUsage)
	}
	for model, usage := range byModel {
		var err error
		total, err = sumCodexModelUsage(total, usage)
		if err != nil {
			return err
		}
		entry, err := sumCodexModelUsage(perModel[model], usage)
		if err != nil {
			return err
		}
		perModel[model] = entry
	}
	m.InputTokens = total.InputTokens
	m.OutputTokens = total.OutputTokens
	m.ReasoningTokens = total.ReasoningTokens
	m.CacheReadInputTokens = total.CacheReadInputTokens
	m.CacheCreationInputTokens = total.CacheCreationInputTokens
	m.CostUnavailable = total.CostUnavailable
	m.PerModelUsage = perModel
	return nil
}
