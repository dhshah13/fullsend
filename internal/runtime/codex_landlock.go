package runtime

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed codex_hook/fullsend-codex-landlock.py
var codexLandlockPy string

type codexWritePolicy struct {
	Protected   []string            `json:"protected"`
	WriteRoots  []string            `json:"write_roots"`
	StatePaths  []string            `json:"state_paths"`
	Files       map[string]string   `json:"files"`
	Directories map[string][]string `json:"directories"`
}

// These are native runtime state, never configuration or executable hooks.
var codexStateDirs = []string{"sessions", "archived_sessions", "thread-writer-locks", "tmp", ".tmp", "shell_snapshots", "log", "cache", "db"}
var codexStateFiles = []string{"installation_id", "session_index.jsonl", codexLastMessageFile}

// codexWriteRoots are the OpenShell read-write paths: everything the agent can write.
var codexWriteRoots = []string{"/sandbox", "/tmp", "/dev/null"}

func codexUnderWriteRoot(path string) bool {
	for _, root := range codexWriteRoots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func codexWritePolicyFor(r CodexRuntime, digests codexRunnerHeldDigestSet) codexWritePolicy {
	policy := codexWritePolicy{
		Protected:   []string{r.ConfigDir(), "/sandbox/codex-policy"},
		WriteRoots:  codexWriteRoots,
		Files:       map[string]string{r.codexConfigPath(): digests.ConfigTOML, r.codexAuthScriptPath(): codexAssetSHA256(codexAuthScriptSH)},
		Directories: map[string][]string{codexRolesDir: {}},
	}
	for _, name := range append(append([]string{}, codexStateDirs...), codexStateFiles...) {
		policy.StatePaths = append(policy.StatePaths, r.ConfigDir()+"/"+name)
	}
	for name, digest := range digests.Roles {
		policy.Files[codexRolesDir+"/"+name] = digest
		policy.Directories[codexRolesDir] = append(policy.Directories[codexRolesDir], name)
	}
	sort.Strings(policy.Directories[codexRolesDir])
	if digests.HooksJSON != "" {
		policy.Files[r.codexHooksPath()] = digests.HooksJSON
	}
	if digests.HookScripts != nil {
		policy.Files[r.codexAdapterPath()] = codexAssetSHA256(codexHookAdapterPy)
		for name, digest := range digests.HookScripts {
			policy.Files[r.codexHooksDir()+"/"+name] = digest
			policy.Directories[r.codexHooksDir()] = append(policy.Directories[r.codexHooksDir()], name)
		}
		sort.Strings(policy.Directories[r.codexHooksDir()])
	}
	return policy
}

// The restriction precedes the shell, including .env. Otherwise a previous
// iteration could leave executable .env content that writes policy before the
// restriction is installed. All its descendants inherit the restriction.
func codexProtectedCommand(command string, digests codexRunnerHeldDigestSet) string {
	policy, _ := json.Marshal(codexWritePolicyFor(CodexRuntime{}, digests)) // string-only fields
	return shellQuote(digests.Python) + " -I -c " + shellQuote(codexLandlockPy) + " " + shellQuote(string(policy)) + " /bin/sh -c " + shellQuote(command)
}
