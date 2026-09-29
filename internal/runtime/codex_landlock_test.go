package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexDispatchPolicy(t *testing.T) {
	for _, input := range []string{`{"hooks":{}}`, `{}`, `{"hooks":null}`} {
		hooks, err := codexAddDispatchGuard([]byte(input), "/usr/bin/python3", nil)
		require.NoError(t, err, input)
		var config codexHooksConfig
		require.NoError(t, json.Unmarshal(hooks, &config))
		matcher := config.Hooks["PreToolUse"][0].Matcher
		// Codex matches a [A-Za-z0-9_|] matcher by exact alternation
		// (codex-rs/hooks/src/events/common.rs), against the names its
		// 0.157.0 and 0.158.0 binaries were observed to send.
		require.Regexp(t, `^[A-Za-z0-9_|]+$`, matcher)
		for _, emitted := range []string{"spawn_agent", "multi_agent_v1resume_agent", "collaborationspawn_agent"} {
			assert.Contains(t, strings.Split(matcher, "|"), emitted, "resume and V2 spawns must reach the guard")
		}
	}
	_, err := codexAddDispatchGuard([]byte("{"), "/usr/bin/python3", nil)
	require.ErrorContains(t, err, "parsing codex hooks")
	for _, tt := range []struct {
		name    string
		payload string
		allowed bool
	}{
		{"fresh named", `{"tool_name":"spawn_agent","tool_input":{"agent_type":"correctness","fork_context":false}}`, true},
		{"fresh generic", `{"tool_name":"spawn_agent","tool_input":{"fork_context":false}}`, true},
		{"unsupported V2", `{"tool_name":"collaborationspawn_agent","tool_input":{"agent_type":"explore","fork_turns":"none","task_name":"inspect","message":"x"}}`, false},
		{"V2 arguments without a namespace", `{"tool_name":"spawn_agent","tool_input":{"agent_type":"explore","fork_turns":"none","task_name":"inspect"}}`, false},
		{"mixed V1 and V2", `{"tool_name":"spawn_agent","tool_input":{"fork_context":false,"fork_turns":"none"}}`, false},
		{"V2 task with V1 freshness", `{"tool_name":"spawn_agent","tool_input":{"fork_context":false,"task_name":"inspect"}}`, false},
		{"parent context", `{"tool_name":"spawn_agent","tool_input":{"fork_context":true}}`, false},
		{"default context", `{"tool_name":"spawn_agent","tool_input":{}}`, false},
		{"V2 full context", `{"tool_name":"spawn_agent","tool_input":{"fork_turns":"all"}}`, false},
		{"model override", `{"tool_name":"spawn_agent","tool_input":{"fork_context":false,"model":"gpt-6-sol"}}`, false},
		{"unknown role", `{"tool_name":"spawn_agent","tool_input":{"fork_context":false,"agent_type":"made-up"}}`, false},
		{"grandchild", `{"tool_name":"spawn_agent","agent_id":"child","tool_input":{"fork_context":false}}`, false},
		{"resume changes policy", `{"tool_name":"multi_agent_v1resume_agent","tool_input":{"id":"closed-child"}}`, false},
		{"resume without a namespace", `{"tool_name":"resume_agent","tool_input":{"id":"closed-child"}}`, false},
		{"invalid payload", `[]`, false},
		{"unrelated tool", `{"tool_name":"Bash","tool_input":{"command":"true"}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(pythonWithTomllib(t), "-I", "-c", codexDispatchPy, `["default","explore","correctness"]`)
			cmd.Stdin = strings.NewReader(tt.payload)
			out, err := cmd.CombinedOutput()
			if tt.allowed {
				require.NoError(t, err, "%s", out)
				assert.Empty(t, out)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				assert.Equal(t, 2, exit.ExitCode(), "Codex treats other nonzero exits as fail-open")
				assert.Contains(t, string(out), "fullsend:")
			}
			switch tt.name {
			case "unsupported V2":
				assert.Contains(t, string(out), "V2 collaboration is unsupported")
			case "resume changes policy":
				assert.Contains(t, string(out), "resuming children")
			}
		})
	}
}

func TestCodexWritePolicyProtectsAllAuthorities(t *testing.T) {
	d := testRunnerHeldDigests
	d.Roles = map[string]string{"default.toml": "digest"}
	p := codexWritePolicyFor(CodexRuntime{}, d)
	assert.Contains(t, p.Protected, CodexRuntime{}.ConfigDir())
	assert.Contains(t, p.Files, CodexRuntime{}.codexConfigPath())
	assert.Contains(t, p.Files, CodexRuntime{}.codexHooksPath())
	assert.Contains(t, p.Files, CodexRuntime{}.codexAdapterPath())
	assert.Equal(t, []string{"default.toml"}, p.Directories[codexRolesDir])
	assert.NotContains(t, p.StatePaths, CodexRuntime{}.OpenAIAuthFile(), "only the runner may refresh the credential file")
	assert.NotContains(t, p.StatePaths, CodexRuntime{}.ConfigDir()+"/models_cache.json")
}

// Run this test on Linux (also cross-compiled and executed in the real
// OpenShell sandbox). It exercises the actual embedded launcher, not a mock.
func TestCodexLandlockEnforcesInheritedProtection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Landlock is a Linux kernel interface; covered by sandbox validation")
	}
	dir := t.TempDir()
	policyDir := filepath.Join(dir, "policy")
	work := filepath.Join(dir, "workspace")
	require.NoError(t, os.Mkdir(policyDir, 0o755))
	require.NoError(t, os.Mkdir(work, 0o755))
	// dir stands in for $HOME=/sandbox: Bootstrap's pre-created entries.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".config", "git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".config", "git", "config"), nil, 0o644))
	role := filepath.Join(policyDir, "default.toml")
	body := []byte("model = \"gpt-5.6-luna\"\n")
	require.NoError(t, os.WriteFile(role, body, 0o644))
	policy := codexWritePolicy{Protected: []string{policyDir}, WriteRoots: []string{dir, "/dev/null"}, StatePaths: []string{}, Files: map[string]string{role: codexAssetSHA256(body)}, Directories: map[string][]string{policyDir: {"default.toml"}}}
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	program := `import errno,os,pathlib,shutil,subprocess,sys
role,work=sys.argv[1:]
home=os.path.dirname(work)
def denied(fn):
 try: fn()
 except OSError as e:
  assert e.errno in (errno.EACCES,errno.EPERM,errno.EXDEV,errno.EBADF),str(e)
 else: raise AssertionError("policy write permitted")
denied(lambda: pathlib.Path(role).write_text("changed"))
denied(lambda: os.truncate(role,0))
denied(lambda: os.unlink(role))
denied(lambda: os.rename(os.path.dirname(role),work+"/moved"))
denied(lambda: pathlib.Path(os.path.dirname(role)+"/new.toml").write_text("new"))
denied(lambda: os.link(role,work+"/hardlink"))
os.symlink(role,work+"/link")
denied(lambda: pathlib.Path(work+"/link").write_text("changed"))
os.chmod(role,0o666)
denied(lambda: pathlib.Path(role).write_text("changed"))
denied(lambda: os.write(3,b"inherited-fd-write"))
pathlib.Path(work+"/normal").write_text("allowed")
denied(lambda: os.mkdir(home+"/.npm"))
os.makedirs(home+"/.config/tool")
env={k:v for k,v in os.environ.items() if not k.startswith(("GIT_","XDG_"))}
if shutil.which("git"):
 subprocess.run(["git","config","--global","user.name","probe"],env={**env,"HOME":home},check=True)
 assert "probe" in pathlib.Path(home+"/.config/git/config").read_text()
child=subprocess.run([sys.executable,"-I","-c","import pathlib,sys;pathlib.Path(sys.argv[1]).write_text('bad')",role],capture_output=True)
assert child.returncode != 0
print("PROTECTION_OK")`
	python := pythonWithTomllib(t)
	// -W error fails on ctypes deprecations, such as 3.14's _pack_ without _layout_.
	cmd := exec.Command(python, "-I", "-W", "error", "-c", codexLandlockPy, string(raw), python, "-I", "-c", program, role, work)
	fd, err := os.OpenFile(role, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer fd.Close()
	cmd.ExtraFiles = []*os.File{fd}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "PROTECTION_OK")
	assert.Equal(t, string(body), readFileString(t, role))
	// Tampering before launch also fails closed, and the protected command
	// never executes; an added role cannot broaden the registered file set.
	// This case runs on the system python3 production resolves (codexPreflightPython).
	require.NoError(t, os.WriteFile(filepath.Join(policyDir, "extra.toml"), body, 0o644))
	system, err := exec.Command("sh", "-c", "command -p -v python3").Output()
	require.NoError(t, err)
	cmd = exec.Command(strings.TrimSpace(string(system)), "-I", "-W", "error", "-c", codexLandlockPy, string(raw), "/bin/echo", "UNEXPECTED")
	out, err = cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "file set modified")
	assert.NotContains(t, string(out), "UNEXPECTED")
}

// The image's PATH leads with the agent-writable /sandbox/.venv. A .pth planted
// there must not reach the interpreter the dispatch guard and launcher run on.
func TestCodexPreflightPythonIgnoresPlantedVenv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resolves the system python3 through a real sh, as the sandbox does")
	}
	bin, venv := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte("#!/bin/sh\nfor last; do :; done\nexec /bin/sh -c \"$last\"\n"), 0o755))
	out, err := exec.Command(pythonWithTomllib(t), "-m", "venv", "--without-pip", venv).CombinedOutput()
	require.NoError(t, err, "%s", out)
	site, err := filepath.Glob(filepath.Join(venv, "lib", "python3*", "site-packages"))
	require.NoError(t, err)
	require.Len(t, site, 1)
	require.NoError(t, os.WriteFile(filepath.Join(site[0], "zz-bypass.pth"), []byte("import os; os._exit(0)\n"), 0o644))
	t.Setenv("PATH", bin+":"+filepath.Join(venv, "bin")+":"+os.Getenv("PATH"))
	guard := func(python string) error {
		cmd := exec.Command(python, "-I", "-c", codexDispatchPy, `["default"]`)
		cmd.Stdin = strings.NewReader(`{"tool_name":"spawn_agent","tool_input":{"fork_context":true}}`)
		return cmd.Run()
	}
	require.NoError(t, guard(filepath.Join(venv, "bin", "python3")), "the planted .pth bypasses the guard")
	python, err := codexPreflightPython("sb")
	require.NoError(t, err)
	var exit *exec.ExitError
	require.ErrorAs(t, guard(python), &exit, "resolved %s", python)
	assert.Equal(t, 2, exit.ExitCode())
}
