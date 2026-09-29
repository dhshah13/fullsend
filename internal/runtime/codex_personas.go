package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed codex_hook/fullsend-codex-dispatch.py
var codexDispatchPy string

// codexDispatchMatcher is an exact alternation of hook tool names. Codex
// canonicalizes only V1 spawn_agent; other namespaced tools reach hooks as
// namespace+name with no separator (codex-rs/core/src/tools/mod.rs
// flat_tool_name): V1 resume is multi_agent_v1resume_agent and V2 spawn is
// collaborationspawn_agent. The plain names cover providers without
// namespaced tools.
const codexDispatchMatcher = "spawn_agent|resume_agent|multi_agent_v1resume_agent|collaborationspawn_agent"

func codexAddDispatchGuard(data []byte, python string, roles []codexPersona) ([]byte, error) {
	var hooks codexHooksConfig
	if err := json.Unmarshal(data, &hooks); err != nil {
		return nil, fmt.Errorf("parsing codex hooks: %w", err)
	}
	if hooks.Hooks == nil {
		hooks.Hooks = map[string][]codexHookMatcherSet{}
	}
	names := make([]string, 0, len(roles))
	for _, role := range roles {
		names = append(names, role.Name)
	}
	roster, _ := json.Marshal(names)
	guard := codexHookMatcherSet{Matcher: codexDispatchMatcher, Hooks: []codexHookEntry{{
		Type: "command", Timeout: 5,
		Command: shellQuote(python) + " -I -c " + shellQuote(codexDispatchPy) + " " + shellQuote(string(roster)),
	}}}
	hooks.Hooks["PreToolUse"] = append([]codexHookMatcherSet{guard}, hooks.Hooks["PreToolUse"]...)
	return json.MarshalIndent(hooks, "", "  ")
}

const codexDefaultSubagentModel = "gpt-5.6-luna"

const codexRolesDir = "/sandbox/codex-policy/roles"

// codexPersona is a native role. Claude model aliases and tool declarations
// never enter its TOML; model selection belongs to the harness configuration.
type codexPersona struct {
	Name, Description, Model, Instructions string
}

func codexPersonas(input BootstrapInput, agentName string) ([]codexPersona, error) {
	// Pi deliberately skips invalid personas for compatibility. Codex is a new
	// consumer and fails closed rather than dispatching a misspelled role as a
	// generic child. Check filesystem types before the shared discovery reads.
	for _, skill := range input.SkillDirs() {
		if skill == "" {
			continue
		}
		dir := filepath.Join(skill, "sub-agents")
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("codex personas: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("codex personas: %s must be a regular directory", dir)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("codex personas: %w", err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".md") && !entry.Type().IsRegular() {
				return nil, fmt.Errorf("codex persona %s must be a regular file", filepath.Join(dir, entry.Name()))
			}
		}
	}
	personas, skipped, err := discoverPersonas(input.SkillDirs(), agentName)
	if err != nil {
		return nil, fmt.Errorf("codex personas: %w", err)
	}
	if len(skipped) != 0 {
		return nil, fmt.Errorf("codex persona %s: %s", skipped[0].Path, skipped[0].Reason)
	}
	models := map[string]string{}
	known := map[string]bool{"default": true, "explore": true}
	for _, persona := range personas {
		known[persona.Name] = true
	}
	keys := make([]string, 0, len(input.AgentSubagents()))
	for key := range input.AgentSubagents() {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value := input.AgentSubagents()[key]
		if value == nil { // A layered-config tombstone removes an override.
			continue
		}
		if !known[key] {
			return nil, fmt.Errorf("codex subagents.%s: no such persona in the selected skills", key)
		}
		model, err := codexPersonaModel(*value)
		if err != nil {
			return nil, fmt.Errorf("codex subagents.%s: %w", key, err)
		}
		models[key] = model
	}
	defaultModel := codexDefaultSubagentModel
	if model := models["default"]; model != "" {
		defaultModel = model
	}
	if value := strings.TrimSpace(os.Getenv("FULLSEND_CODEX_SUBAGENT_MODEL")); value != "" {
		model, err := codexPersonaModel(value)
		if err != nil {
			return nil, fmt.Errorf("FULLSEND_CODEX_SUBAGENT_MODEL: %w", err)
		}
		defaultModel = model
	}
	resolve := func(name string) string {
		if name != "default" && models[name] != "" {
			return models[name]
		}
		return defaultModel
	}
	roles := []codexPersona{
		{
			Name: "default", Description: "Investigate the explicit task package supplied by the parent.",
			Model: defaultModel, Instructions: codexChildInstructions,
		},
		{
			Name: "explore", Description: "Read-only exploration and evidence gathering.",
			Model: resolve("explore"), Instructions: "Explore using read-only tools. Do not edit files or execute state-changing commands.\n\n" + codexChildInstructions,
		},
	}
	for _, persona := range personas {
		if persona.Description == "" {
			return nil, fmt.Errorf("codex persona %s: frontmatter description is required (Codex drops a role without one)", persona.Name)
		}
		if persona.Model != "" {
			fmt.Fprintf(os.Stderr, "Codex persona %q: ignoring Claude frontmatter model %q; resolved model %q\n", persona.Name, persona.Model, resolve(persona.Name))
		}
		if len(persona.Tools) != 0 || len(persona.BashAllowlist) != 0 {
			fmt.Fprintf(os.Stderr, "Codex persona %q: Claude frontmatter tools are documentation only; shared security hooks still apply\n", persona.Name)
		}
		roles = append(roles, codexPersona{
			Name: persona.Name, Description: persona.Description,
			Model: resolve(persona.Name), Instructions: persona.Body + "\n\n" + codexChildInstructions,
		})
	}
	slices.SortFunc(roles, func(a, b codexPersona) int { return strings.Compare(a.Name, b.Name) })
	return roles, nil
}

func codexPersonaModel(value string) (string, error) {
	value = strings.TrimSpace(value)
	model, err := translateCodexModel(value)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(model, "inherit") || codexClaudeAliases[strings.ToLower(model)] || strings.ContainsAny(model, "/ \t\r\n") {
		return "", fmt.Errorf("an explicit OpenAI model ID is required, got %q", value)
	}
	return model, nil
}

func (p codexPersona) config() []byte {
	return []byte("# Written by fullsend; do not edit.\nmodel = " + codexTOMLString(p.Model) +
		"\ndeveloper_instructions = " + codexTOMLString(p.Instructions) + "\n")
}

func codexPersonaConfig(roles []codexPersona, dir string) string {
	if len(roles) == 0 {
		// The model's catalog entry overrides [features] multi_agent; only
		// this removes spawn_agent (verified on 0.157.0 and 0.158.0).
		return "\n[agents]\nenabled = false\n"
	}
	var b strings.Builder
	b.WriteString("\n[agents]\nmax_concurrent_threads_per_session = 4\nmax_depth = 1\n")
	for _, role := range roles {
		if role.Name == "default" {
			fmt.Fprintf(&b, "default_subagent_model = %s\n", codexTOMLString(role.Model))
		}
	}
	for _, role := range roles {
		fmt.Fprintf(&b, "\n[agents.%s]\ndescription = %s\nconfig_file = %s\n", codexTOMLString(role.Name), codexTOMLString(role.Description), codexTOMLString(dir+"/"+role.Name+".toml"))
	}
	return b.String()
}

const codexChildInstructions = `Work only on the explicit task supplied by the parent. Return your evidence and result to the parent. Do not dispatch further children. Do not write the parent's final agent-result.json; synthesis and final output belong to the parent.`

const codexSubagentNote = `
## Native Codex delegation

This agent runs on FULLSEND_RUNTIME=codex.

Use native spawn_agent for skill-requested delegation. Named personas use the matching agent_type; generic tasks use default; Explore tasks use explore. Explore is an instruction to use read-only operations, not an additional filesystem sandbox. The runtime selects child models: omit model and reasoning overrides, and never translate Claude aliases or pass inherit.

Give each child a complete, explicit task package and fresh context using the V1 tool schema with fork_context=false. Delegation requires a V1 parent: the parent model's metadata selects its tool API, independently of the selected child model. V2 arguments (fork_turns or task_name) are rejected, with no automatic fallback. If the session exposes V2 tools, report delegation as unsupported.

At most four children may be open at once. Dispatch larger batches in waves. Wait can return when only ONE child finishes: keep waiting for every outstanding child, collect each result, and close each finished child (completed, errored, shutdown or not_found) with close_agent before opening another slot. Never close a still-running child to collect its result. Missing results, failed children, and cancellation are not successful completion. Keep root synthesis and agent-result.json with the parent. Start any challenger only after collecting the original findings, with a new task and fresh context. Children must not dispatch grandchildren.

Do not resume closed children: native resume does not preserve the selected role policy. Spawn a new child with an explicit fresh task instead.

Before writing the final output, confirm that EVERY spawned child ID, including the final challenger, has finished and been closed. A final child still needs closing even when no further slot will be opened. Do not wait on or close an already-closed ID again.
`
