package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexPersonaInput struct {
	bootstrapInput
	subagents map[string]*string
}

func (b codexPersonaInput) AgentSubagents() map[string]*string { return b.subagents }

func codexPersonaSkill(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub-agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub-agents", name+".md"), []byte(body), 0o644))
	return dir
}

func codexParseTOML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	cmd := exec.Command(pythonWithTomllib(t), "-c", "import json,sys,tomllib; print(json.dumps(tomllib.loads(sys.stdin.read())))")
	cmd.Stdin = strings.NewReader(string(data))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var result map[string]any
	require.NoError(t, json.Unmarshal(out, &result))
	return result
}

func TestCodexBootstrapPersonaModels(t *testing.T) {
	ptr := func(s string) *string { return &s }
	for _, tt := range []struct {
		name, envDefault, wantNamed, wantGeneric string
		settings                                 map[string]*string
	}{
		{name: "cheap default ignores Claude frontmatter", wantNamed: "gpt-5.6-luna", wantGeneric: "gpt-5.6-luna"},
		{name: "configured default", settings: map[string]*string{"default": ptr("openai/gpt-5.5")}, wantNamed: "gpt-5.5", wantGeneric: "gpt-5.5"},
		{name: "child model does not select parent tool API", settings: map[string]*string{"default": ptr("openai/gpt-6-sol")}, wantNamed: "gpt-6-sol", wantGeneric: "gpt-6-sol"},
		{name: "environment overrides configured default", envDefault: "openai/gpt-5.6-luna", settings: map[string]*string{"default": ptr("gpt-5.5")}, wantNamed: "gpt-5.6-luna", wantGeneric: "gpt-5.6-luna"},
		{name: "persona overrides environment", envDefault: "gpt-5.6-luna", settings: map[string]*string{"correctness": ptr("openai/gpt-5.5")}, wantNamed: "gpt-5.5", wantGeneric: "gpt-5.6-luna"},
		{name: "tombstone removes persona override", envDefault: "gpt-5.6-luna", settings: map[string]*string{"correctness": nil, "default": ptr("gpt-5.5")}, wantNamed: "gpt-5.6-luna", wantGeneric: "gpt-5.6-luna"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FULLSEND_CODEX_SUBAGENT_MODEL", tt.envDefault)
			store := t.TempDir()
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), store, "codex-cli 0.157.0")
			skill := codexPersonaSkill(t, "correctness", "---\nname: correctness\ndescription: Find defects.\nmodel: opus\ntools: Read\n---\nReturn only independently verified defects.\n")
			r := CodexRuntime{}
			err := r.Bootstrap(codexPersonaInput{bootstrapInput: bootstrapInput{
				sandboxName: "personas", agentPath: writeAgentFile(t, codexTestReviewAgentDef), agentName: "review", skillDirs: []string{skill},
			}, subagents: tt.settings})
			require.NoError(t, err)
			cfg := codexParseTOML(t, storedUpload(t, store, r.codexConfigPath()))
			agents, ok := cfg["agents"].(map[string]any)
			require.True(t, ok, "Bootstrap must register native children")
			assert.NotContains(t, agents, "enabled", "Codex's default keeps multi-agent tools on")
			assert.Equal(t, tt.wantGeneric, agents["default_subagent_model"])
			assert.Equal(t, float64(4), agents["max_concurrent_threads_per_session"])
			assert.Equal(t, float64(1), agents["max_depth"])
			for name, model := range map[string]string{"correctness": tt.wantNamed, "default": tt.wantGeneric, "explore": tt.wantGeneric} {
				decl, ok := agents[name].(map[string]any)
				require.True(t, ok, "role %s missing", name)
				rolePath, ok := decl["config_file"].(string)
				require.True(t, ok)
				role := codexParseTOML(t, storedUpload(t, store, rolePath))
				assert.Equal(t, model, role["model"], "role %s", name)
				if name == "correctness" {
					assert.Contains(t, role["developer_instructions"], "Return only independently verified defects.")
					assert.Equal(t, "Find defects.", decl["description"])
				}
				assert.NotContains(t, role, "tools", "Claude fields must not enter native TOML")
			}
		})
	}
}

func TestCodexBootstrapRejectsInvalidPersonaConfiguration(t *testing.T) {
	for _, tt := range []struct{ name, model string }{
		{"Claude alias", "opus"}, {"prefixed alias", "openai/opus"}, {"inherit is not a model", "inherit"}, {"prefixed inherit", "OPENAI/inherit"}, {"other provider", "anthropic/claude-opus"}, {"blank", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FULLSEND_CODEX_SUBAGENT_MODEL", "")
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.157.0")
			err := (CodexRuntime{}).Bootstrap(codexPersonaInput{bootstrapInput: bootstrapInput{sandboxName: "invalid", agentPath: writeAgentFile(t, codexTestReviewAgentDef)}, subagents: map[string]*string{"default": &tt.model}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "subagents.default")
		})
	}
}

func TestCodexPersonaSerializationAndGenericRoles(t *testing.T) {
	t.Setenv("FULLSEND_CODEX_SUBAGENT_MODEL", "")
	roles, err := codexPersonas(codexPersonaInput{bootstrapInput: bootstrapInput{skillDirs: []string{"", t.TempDir()}}}, "retro")
	require.NoError(t, err)
	require.Len(t, roles, 2, "retro can delegate without persona files")
	assert.Equal(t, "default", roles[0].Name)
	assert.Equal(t, "explore", roles[1].Name)
	instructions := "quoted \"\"\"\n[agents.evil]\nmodel = \"opus\"\nbackslash \\ and tab\t"
	role := codexPersona{Name: "test", Description: "Test", Model: "gpt-5.6-luna", Instructions: instructions}
	parsed := codexParseTOML(t, role.config())
	assert.Equal(t, instructions, parsed["developer_instructions"])
	assert.NotContains(t, parsed, "agents")
}

func TestCodexPersonaInvalidDiscoveryAndSettings(t *testing.T) {
	for _, body := range []string{"---\nname: [broken\n---\nBody", "---\nname: mismatch\n---\nBody", "Body with no name"} {
		_, err := codexPersonas(codexPersonaInput{bootstrapInput: bootstrapInput{skillDirs: []string{codexPersonaSkill(t, "test", body)}}}, "retro")
		require.Error(t, err)
	}
	_, err := codexPersonas(codexPersonaInput{bootstrapInput: bootstrapInput{skillDirs: []string{codexPersonaSkill(t, "test", "---\nname: test\n---\nBody")}}}, "retro")
	require.ErrorContains(t, err, "description is required", "Codex silently drops a role with a blank description")
	file := filepath.Join(t.TempDir(), "not-a-skill-dir")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	_, err = codexPersonas(codexPersonaInput{bootstrapInput: bootstrapInput{skillDirs: []string{file}}}, "retro")
	require.ErrorContains(t, err, "codex personas:", "an unreadable skill path fails closed")
	if os.Geteuid() != 0 {
		roster := filepath.Join(codexPersonaSkill(t, "correctness", "---\nname: correctness\ndescription: Find defects.\n---\nBody"), "sub-agents")
		require.NoError(t, os.Chmod(roster, 0))
		t.Cleanup(func() { _ = os.Chmod(roster, 0o755) })
		_, err = codexPersonas(codexPersonaInput{bootstrapInput: bootstrapInput{skillDirs: []string{filepath.Dir(roster)}}}, "retro")
		require.ErrorContains(t, err, "codex personas:", "an unreadable roster fails closed; pi only skips it")
	}
	model := "gpt-5.6-luna"
	_, err = codexPersonas(codexPersonaInput{subagents: map[string]*string{"unknown": &model}}, "retro")
	require.ErrorContains(t, err, "no such persona")
	t.Setenv("FULLSEND_CODEX_SUBAGENT_MODEL", "inherit")
	_, err = codexPersonas(codexPersonaInput{}, "retro")
	require.ErrorContains(t, err, "FULLSEND_CODEX_SUBAGENT_MODEL")
}

func TestCodexBootstrapRejectsDuplicateAndSymlinkPersonas(t *testing.T) {
	definition := "---\nname: correctness\ndescription: Find defects.\n---\nFind defects.\n"
	for _, kind := range []string{"duplicate", "file symlink", "directory symlink", "reserved name"} {
		t.Run(kind, func(t *testing.T) {
			fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "codex-cli 0.157.0")
			skill := codexPersonaSkill(t, "correctness", definition)
			dirs := []string{skill}
			switch kind {
			case "duplicate":
				dirs = append(dirs, codexPersonaSkill(t, "correctness", definition))
			case "file symlink":
				file := filepath.Join(skill, "sub-agents", "correctness.md")
				require.NoError(t, os.Rename(file, filepath.Join(skill, "target.md")))
				require.NoError(t, os.Symlink("../target.md", file))
			case "directory symlink":
				require.NoError(t, os.Rename(filepath.Join(skill, "sub-agents"), filepath.Join(skill, "target")))
				require.NoError(t, os.Symlink("target", filepath.Join(skill, "sub-agents")))
			case "reserved name":
				dirs = []string{codexPersonaSkill(t, "default", "---\nname: default\n---\nOverride native default.\n")}
			}
			err := (CodexRuntime{}).Bootstrap(bootstrapInput{sandboxName: "bad-persona", agentPath: writeAgentFile(t, codexTestReviewAgentDef), skillDirs: dirs})
			require.Error(t, err)
		})
	}
}
