package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// fakeOpenshellCodex installs a fake "openshell" that records every argv line
// to logPath, stores each upload payload under storeDir keyed by the remote
// path, answers `codex --version` and `cat <remote>` execs from that store,
// and streams streamFixture (when non-empty) for the codex run command.
// Everything else succeeds silently.
func fakeOpenshellCodex(t *testing.T, logPath, storeDir, version string, streamFixture ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(storeDir, 0o755))
	streamCase := "exit 0"
	if len(streamFixture) > 0 && streamFixture[0] != "" {
		streamCase = "cat '" + streamFixture[0] + "'; exit 0"
	}
	findCase := "exit 0"
	if len(streamFixture) > 1 && streamFixture[1] != "" {
		findCase = "echo '" + streamFixture[1] + "'; exit 0"
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
if [ "$2" = "download" ]; then
  base=$(basename "$4")
  mkdir -p "$5" 2>/dev/null
  if [ -d "$5" ]; then printf 'fixture\n' > "$5/$base"; else printf 'fixture\n' > "$5"; fi
  exit 0
fi
if [ "$2" = "upload" ]; then
  cp "$4" '` + storeDir + `'/"$(printf '%s' "$5" | tr '/' '_')"
  exit 0
fi
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    "codex --version") echo "codex-cli ` + version + `"; exit 0 ;;
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + storeDir + `'/"$f"; exit $? ;;
    *"exec --json"*) ` + streamCase + ` ;;
    find\ *) ` + findCase + ` ;;
  esac
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type codexHooksBootstrapInput struct {
	bootstrapInput
	hooks security.SandboxHookConfig
}

func (b codexHooksBootstrapInput) SandboxHookConfig() security.SandboxHookConfig { return b.hooks }

const codexTestAgentDef = `---
name: triage
description: Inspect an issue.
tools: Bash(gh,jq),Read,Skill
model: openai/gpt-5.6-luna
---
You are the triage agent. Use gh.
`

func TestCodexRuntimeBootstrap_WritesConfigAndManifest(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1")

	r := CodexRuntime{}
	err := r.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
	})
	require.NoError(t, err)

	// config.toml carries the agent body as developer_instructions.
	cfg := string(storedUpload(t, storeDir, r.codexConfigPath()))
	assert.Contains(t, cfg, `model_provider = "`+codexProviderID+`"`)
	assert.Contains(t, cfg, "# Agent: triage")
	assert.Contains(t, cfg, "You are the triage agent. Use gh.")
	assert.Contains(t, cfg, "FULLSEND_RUNTIME=codex", "the runtime note tells skills which runtime they are on")

	// The auth script is uploaded byte-identical to the embedded copy — the
	// run guard pins its SHA-256 — and made executable, which uploadBytes
	// does not do on its own.
	assert.Equal(t, codexAuthScriptSH, storedUpload(t, storeDir, r.codexAuthScriptPath()))
	assert.Contains(t, readFileString(t, logPath), "chmod 755 '"+r.codexAuthScriptPath()+"'")

	var m codexManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, storeDir, r.codexManifestPath()), &m))
	assert.Equal(t, "triage", m.AgentName)
	assert.Equal(t, "openai/gpt-5.6-luna", m.Model)
	assert.Equal(t, "codex-cli 0.152.1", m.CodexVersion)
	// Tools stay in Claude vocabulary: codex has no native allowlist, so this
	// is what FULLSEND_TOOL_ALLOWLIST and the allowlist hook match on (#608).
	assert.Equal(t, []string{"Bash", "Read", "Skill"}, m.Tools)
	assert.Equal(t, []string{"gh", "jq"}, m.BashAllowlist)
	assert.Nil(t, m.Hooks, "no hook plan when the input carries no sandbox hook config")

	// Without SandboxHooksBootstrap nothing hook-related is installed.
	log := readFileString(t, logPath)
	assert.NotContains(t, log, codexAdapterFile)
	assert.NotContains(t, log, codexHooksFile)
}

func TestCodexRuntimeBootstrap_RejectsAgentNameMismatch(t *testing.T) {
	fakeOpenshellCodex(t, filepath.Join(t.TempDir(), "log"), t.TempDir(), "0.152.1")

	err := CodexRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "review",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent name mismatch")
}

func TestCodexRuntimeBootstrap_InstallsHooksWiringAndAdapter(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1")

	r := CodexRuntime{}
	err := r.Bootstrap(codexHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, codexTestAgentDef),
			agentName:   "triage",
		},
		hooks: security.SandboxHookConfigFromHarness(&harness.Harness{}),
	})
	require.NoError(t, err)

	// The adapter lands byte-identical to the embedded copy: the run guard
	// pins its SHA-256, so any drift would fail every iteration closed.
	assert.Equal(t, codexHookAdapterPy, storedUpload(t, storeDir, r.codexAdapterPath()))

	// Every hook script from the plan is installed beside it, where the
	// adapter resolves them from its own location.
	log := readFileString(t, logPath)
	for name := range security.HookFiles(security.SandboxHookConfigFromHarness(&harness.Harness{})) {
		assert.Contains(t, log, r.codexHooksDir()+"/"+name, "hook script %s was not installed", name)
	}

	var hooksJSON codexHooksConfig
	require.NoError(t, json.Unmarshal(storedUpload(t, storeDir, r.codexHooksPath()), &hooksJSON))
	assert.NotEmpty(t, hooksJSON.Hooks[string(security.HookPhasePreToolUse)])
	assert.NotEmpty(t, hooksJSON.Hooks[string(security.HookPhasePostToolUse)])

	var m codexManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, storeDir, r.codexManifestPath()), &m))
	require.NotNil(t, m.Hooks)
	assert.Equal(t, r.codexHooksDir(), m.Hooks.Dir)
	assert.NotEmpty(t, m.Hooks.Groups)
	assert.Equal(t, "Edit", m.Hooks.ToolNames["apply_patch"])

	// The PostToolUseFailure group is recorded as seen but not wired: codex
	// has no such event, and its PostToolUse already fires for failed
	// commands, so nothing is lost.
	var sawFailurePhase bool
	for _, g := range m.Hooks.Groups {
		if g.Phase == string(security.HookPhasePostToolUseFailure) {
			sawFailurePhase = true
			assert.False(t, g.Wired)
		}
	}
	assert.True(t, sawFailurePhase, "the plan's failure group must be recorded, not dropped silently")
}

func TestCodexRuntimeBootstrap_UploadsSkillsAndWarnsOnPlugins(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "0.152.1")

	skillDir := filepath.Join(t.TempDir(), "code-review")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: code-review\n---\nbody\n"), 0o644))

	r := CodexRuntime{}
	err := r.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
		skillDirs:   []string{skillDir},
		pluginDirs:  []string{"/plugins/example"},
	})
	require.NoError(t, err)

	// codex discovers $CODEX_HOME/skills natively.
	assert.Contains(t, readFileString(t, logPath), r.ConfigDir()+"/skills/")
}

func TestCodexRuntimeBootstrap_PreflightFailureIsReportedEarly(t *testing.T) {
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    "codex --version") echo "codex: command not found" >&2; exit 127 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := CodexRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, codexTestAgentDef),
		agentName:   "triage",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex preflight")
	assert.Contains(t, err.Error(), "exited 127")
}

func TestCodexUnsupportedTools(t *testing.T) {
	t.Parallel()

	// codex does reading, searching and fetching through its shell, so these
	// entries are documentation rather than capabilities it lacks entirely.
	assert.Equal(t, []string{"Read", "Grep", "WebFetch"},
		codexUnsupportedTools([]string{"Bash", "Read", "Grep", "WebFetch", "Write", "Skill"}))
	assert.Empty(t, codexUnsupportedTools([]string{"Bash", "Write", "Edit"}))
	assert.Empty(t, codexUnsupportedTools(nil))
}

func TestCodexDeveloperInstructions(t *testing.T) {
	t.Parallel()

	def, err := parsePiAgent([]byte(codexTestAgentDef))
	require.NoError(t, err)
	got := codexDeveloperInstructions("triage", def)

	assert.Contains(t, got, "# Agent: triage")
	assert.Contains(t, got, "Inspect an issue.")
	assert.Contains(t, got, "You are the triage agent. Use gh.")
	// Skills written for Claude Code's Agent tool must take their
	// single-context path deliberately rather than recording a failed
	// dispatch (the same note pi carries, #6527).
	assert.Contains(t, got, "No fullsend sub-agent roster is available")
}

func TestReadCodexManifest_RejectsGarbage(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	storeDir := t.TempDir()
	fakeOpenshellCodex(t, logPath, storeDir, "0.152.1")

	r := CodexRuntime{}
	path := r.codexManifestPath()
	require.NoError(t, os.WriteFile(
		filepath.Join(storeDir, strings.ReplaceAll(path, "/", "_")), []byte("not json"), 0o644))

	_, err := readCodexManifest("sb", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding codex manifest")
}

func TestCodexClearIterationArtifacts_SweepsSessionsAndLogs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "openshell.log")
	fakeOpenshellCodex(t, logPath, t.TempDir(), "0.152.1")

	r := CodexRuntime{}
	require.NoError(t, r.ClearIterationArtifacts("sb"))

	log := readFileString(t, logPath)
	// The stray-process sweep runs first, so a process the previous iteration
	// left behind cannot write into the directories being cleared.
	assert.Contains(t, log, shellQuote(r.codexSessionsDir())+"/*")
	assert.Contains(t, log, shellQuote(sandbox.SandboxWorkspace)+"/output/*")
	assert.Contains(t, log, codexDebugLogFile)
}

// readFileString returns the file's contents, or "" when it does not exist:
// a fake-openshell log only appears once something actually invoked it, and
// "nothing ran" is exactly what several of these tests assert.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}
