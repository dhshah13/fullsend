package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// codexHooksMissingExit is the exit code the run command uses when the hook
// adapter, the hook wiring or the manifest is not where Bootstrap put it, or
// the adapter no longer matches the copy embedded in this binary. codex loads
// hooks.json silently — a missing file is simply "no hooks" — so without this
// guard a deleted or edited adapter would give a hookless iteration that looks
// perfectly healthy.
const codexHooksMissingExit = 97

// codexConfigTamperedExit is the exit code of the config-dir integrity guard:
// config.toml or the auth script was changed in a way that could redirect the
// model call or replace the credential. Distinct from codexHooksMissingExit so
// Run can name the actual cause.
const codexConfigTamperedExit = 98

// codexWriteProtectionExit is the Landlock launcher's sys.exit(78): no Landlock
// ABI 3+, or a runner-held file changed before launch.
const codexWriteProtectionExit = 78

// codexBinaryVar holds the absolute path of the codex binary, resolved before
// .env is sourced and marked read-only.
const codexBinaryVar = "FULLSEND_CODEX_BIN"

// codexRuntimeEnv tells skills running inside the sandbox which runtime they
// are on, so a skill can take a runtime-specific path deliberately.
const codexRuntimeEnv = "FULLSEND_RUNTIME"

// codexPathVar holds the PATH captured before the agent-writable .env is
// sourced, so it can be restored before codex — and therefore every hook — is
// launched.
const codexPathVar = "FULLSEND_CODEX_PATH"

// codexHookDigestsEnv carries the hook scripts' expected digests to the
// adapter as "<name>:<sha256>" pairs, space separated.
//
// The run command exports it after .env and immediately before codex starts,
// so its value is fixed in the codex process's environment for the whole
// iteration: the agent can rewrite a hook script mid-run, but it cannot reach
// into an already-running process's environment to move the digest it will be
// checked against. That is what lets the adapter re-verify each script at
// every invocation rather than trusting the once-per-launch shell guard —
// closing the intra-iteration window that Claude Code and pi leave open.
const codexHookDigestsEnv = "FULLSEND_CODEX_HOOK_DIGESTS"

// codexHookDigestsValue renders the map for the environment. Sorted so the
// launch command is stable across iterations.
func codexHookDigestsValue(scripts map[string]string) string {
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+":"+scripts[name])
	}
	return strings.Join(pairs, " ")
}

// codexOpenAIProvider is the only model provider prefix codex accepts in a
// fullsend model spec. codex speaks the OpenAI Responses API and has no
// Vertex, Anthropic or Gemini path, so any other prefix is a configuration
// error rather than something to translate.
const codexOpenAIProvider = "openai"

// codexReasoningEfforts are the values codex accepts for
// model_reasoning_effort (codex-rs/protocol/src/openai_models.rs
// ReasoningEffort, rust-v0.152.1). fullsend's own effort vocabulary
// (config.ValidEffortLevels: low, medium, high, xhigh, max) is a subset, so
// the mapping is the identity — an earlier draft remapped max to xhigh, which
// 0.152.1 makes unnecessary. "off" is accepted as an alias for codex's "none"
// because pi's thinking vocabulary uses it.
var codexReasoningEfforts = map[string]string{
	"off":     "none",
	"none":    "none",
	"minimal": "minimal",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "xhigh",
	"max":     "max",
}

// codexEffortFor returns the model_reasoning_effort override for a harness
// effort value, and whether it was recognised. An empty effort yields "" so
// the override is omitted and codex's own default (medium) applies — codex
// resolves the default per model, so pinning one here would override a
// model's own preference for no reason.
func codexEffortFor(effort string) (string, bool) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return "", true
	}
	if mapped, ok := codexReasoningEfforts[strings.ToLower(effort)]; ok {
		return mapped, true
	}
	return "", false
}

// codexClaudeAliases are the Claude-vocabulary model aliases a fleet harness
// or an agents: entry may carry (docs/runtimes.md). They are Claude models and
// deliberately do not apply to codex: a repo on `runtime: codex` names an
// OpenAI id instead. Keeping them out is a decision, not an omission — codex
// must not consult the per-repo `models.aliases` overrides either, so a Claude
// alias can never resolve to a GPT model behind the operator's back.
var codexClaudeAliases = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true}

// codexModelHelp names both ways to give codex a model it can serve.
const codexModelHelp = "set FULLSEND_CODEX_MODEL=" + codexOpenAIProvider +
	"/<id> for the repo, or model: " + codexOpenAIProvider +
	"/<id> on the agent's agents: entry or the harness"

// translateCodexModel resolves a model spec into codex's --model value: a bare
// id passes through and an `openai/` prefix is stripped. A Claude alias or any
// other provider prefix is an error naming both fixes, because codex would
// otherwise send the whole string as a model id and get a 404 from OpenAI with
// nothing to tune — which is exactly what the local smoke showed: five error
// reconnects and a turn.failed carrying "Model not found".
func translateCodexModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("codex takes OpenAI model ids only and no model was named: %s", codexModelHelp)
	}
	provider, id, hasSlash := strings.Cut(model, "/")
	if !hasSlash {
		if codexClaudeAliases[strings.ToLower(model)] {
			return "", fmt.Errorf(
				"codex takes OpenAI model ids only, and the Claude model aliases do not apply to it: %q is one of them. To run this agent on codex, %s",
				model, codexModelHelp)
		}
		return model, nil
	}
	if !strings.EqualFold(provider, codexOpenAIProvider) {
		return "", fmt.Errorf(
			"codex takes OpenAI model ids only, so %q is not available on it: %s",
			model, codexModelHelp)
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("model %q has an empty model id after the %q prefix: %s", model, codexOpenAIProvider, codexModelHelp)
	}
	return id, nil
}

// codexBinaryPin is the POSIX sh fragment that records where codex is.
// `command -v` is a builtin; `readonly` is a special builtin, so a later
// assignment in a sourced file is an error: under a POSIX sh such as dash
// (what `sh -c` is in the sandbox image) it aborts the sourcing shell, and
// under any shell the assignment fails and the pinned value stands. The launch
// below uses the path, which no function or alias can shadow. This matters
// more on codex than on pi: `codex` on PATH is npm's node launcher, so a
// planted shim would run before the native binary ever starts.
func codexBinaryPin() string {
	return `readonly ` + codexBinaryVar + `="$(command -v codex)" && test -n "$` + codexBinaryVar +
		`" || { echo 'fullsend: codex not found on PATH' >&2; exit 127; }`
}

// codexAssetGuard is the POSIX sh fragment run before codex: the runner-owned
// files must exist, and the two whose contents are fixed at compile time — the
// hook adapter and the provider auth script — must be byte-identical to the
// copies embedded in this binary. Landlock protects the launched hierarchy;
// these runner-held checks also detect external or pre-launch changes.
//
// `command -p` bypasses shell functions and uses the system default PATH, so
// nothing left in the environment can stand in for sha256sum or cut; test, [
// and echo are builtins.
func codexAssetGuard(r CodexRuntime, hooksEnabled bool, digests codexRunnerHeldDigestSet) string {
	checks := []string{
		"test -f " + shellQuote(r.codexConfigPath()),
		"test -f " + shellQuote(r.codexAuthScriptPath()),
		codexSHACheck(r.codexAuthScriptPath(), codexAssetSHA256(codexAuthScriptSH)),
	}
	if hooksEnabled {
		checks = append(checks,
			"test -f "+shellQuote(r.codexHooksPath()),
			"test -f "+shellQuote(r.codexManifestPath()),
			"test -f "+shellQuote(r.codexAdapterPath()),
			codexSHACheck(r.codexAdapterPath(), codexAssetSHA256(codexHookAdapterPy)),
			// Bind every installed script to its name, including against
			// external changes before the protected native hierarchy starts.
			codexHookScriptsGuard(r.codexHooksDir(), digests.HookScripts),
		)
	}
	return fmt.Sprintf(
		`{ %s || { echo 'fullsend: codex config, hook adapter or auth script missing or modified; refusing to run' >&2; exit %d; }; }`,
		strings.Join(checks, " && "), codexHooksMissingExit)
}

func codexSHACheck(path, expected string) string {
	return `[ "$(command -p sha256sum ` + shellQuote(path) + ` | command -p cut -d' ' -f1)" = ` + shellQuote(expected) + ` ]`
}

// codexConfigGuard is the POSIX sh fragment that fails closed when a
// runner-owned, per-run file under CODEX_HOME is no longer byte-for-byte what
// Bootstrap uploaded. The expected values are runner-held digests
// (codex_integrity.go), so nothing in the agent-writable config directory
// contributes to the answer.
//
// An earlier version checked config.toml with grep line patterns instead —
// that base_url is the pinned one, that no `[projects` header appears — and it
// was wrong in a way worth recording: TOML's dotted-key form,
// `projects."<repo>".trust_level = "trusted"`, sets the same value with no
// bracket header and slipped straight past it. That line makes codex load the
// target repo's own `.codex/config.toml`, which then supplies
// `developer_instructions`, `model` and, under
// `--dangerously-bypass-hook-trust`, repo-authored hooks. Verified against
// codex 0.152.1: with the line the repo layer applied, without it it did not,
// and `-c projects={}` and a `-c` scalar `trust_level="untrusted"` both failed
// to override it. A whole-file digest has no such blind spot.
func codexConfigGuard(r CodexRuntime, digests codexRunnerHeldDigestSet) string {
	checks := []string{codexSHACheck(r.codexConfigPath(), digests.ConfigTOML)}
	if digests.HooksJSON != "" {
		checks = append(checks, codexSHACheck(r.codexHooksPath(), digests.HooksJSON))
	}
	return fmt.Sprintf(
		`{ %s || { echo 'fullsend: codex config.toml or hooks.json is not the file fullsend wrote; refusing to run (a rewritten config can trust the target repo, which loads its .codex/ layer and its hooks)' >&2; exit %d; }; }`,
		strings.Join(checks, " && "), codexConfigTamperedExit)
}

// buildCodexRunCommand renders the in-sandbox command line.
//
// Security-relevant choices, in the order they appear:
//
//   - the binary is resolved and pinned read-only before the agent-writable
//     .env is sourced, and the guards run there too, where nothing can shadow
//     the shell builtins they use;
//   - the credential is seeded into a runner-owned file before .env can
//     replace OPENAI_API_KEY with another provider's placeholder;
//   - after .env, CODEX_HOME is re-pinned and the variables that could
//     redirect the endpoint (OPENAI_BASE_URL), supply a second credential
//     (CODEX_API_KEY, OPENAI_API_KEY) or load code into the node launcher
//     (NODE_OPTIONS, NODE_PATH) are unset, then the config guard runs again;
//   - approval policy, sandbox mode and the model provider are passed as `-c`
//     SessionFlag overrides, which sit above every config layer, so no
//     rewritten file can move them;
//   - the project's trust is pinned to untrusted in config.toml, so the target
//     repo's own .codex/ layer — including repo-owned hooks — never loads;
//   - whether the hook adapter is required is decided from the runner's own
//     signal (params.HooksSettingsPath, the same one ClaudeRuntime uses for
//     --settings), never from the agent-writable manifest.
func buildCodexRunCommand(params RunParams, model, effort string, hooksEnabled bool, digests codexRunnerHeldDigestSet) string {
	r := CodexRuntime{}
	envFile := sandbox.SandboxWorkspace + "/.env"

	parts := []string{"cd " + shellQuote(params.RepoDir)}
	parts = append(parts,
		"&& "+codexBinaryPin(),
		// PATH is captured before .env and restored after it. The hook scripts
		// resolve their tools by name — tirith_check.py runs a bare `tirith` —
		// so a .env that prepends a directory holding a fake `tirith` that
		// exits 0 neuters the whole PreToolUse chain while every digest stays
		// green. Reproduced before this was added. `readonly` means a .env
		// that assigns the same name aborts the sourcing shell under a POSIX
		// sh rather than winning.
		"&& readonly "+codexPathVar+`="$PATH"`,
		"&& "+codexAssetGuard(r, hooksEnabled, digests),
		"&& "+codexConfigGuard(r, digests),
		"&& . "+shellQuote(envFile),
		// .env is agent-writable; re-pin the runner-owned config location
		// after it so a rewritten .env cannot move codex's home out from
		// under the guards.
		"&& "+strings.Join(r.EnvExports(), " && "),
		// Keep the npm launcher's env-node interpreter, provider auth and hooks
		// on the trusted base PATH. Writable helpers belong only in tool shells.
		"&& export PATH=\"$"+codexPathVar+"\" && export "+codexPathVar,
		"&& export "+codexRuntimeEnv+"=codex",
		// NODE_* would load code into npm's codex launcher; PYTHON* would do
		// the same to the hook adapter's interpreter, which `-I` in hooks.json
		// already isolates — belt and braces, since the adapter is the one
		// process that decides whether a tool call is allowed.
		// LD_* would load code into any dynamically linked binary the run
		// starts — codex's own native binary, tirith, git — before its main
		// runs, which no digest of ours would see.
		// Native hooks invoke the image's shell non-interactively. Bash still
		// sources BASH_ENV in that mode, so a planted startup script could exit
		// before the mandatory guard even with allow_login_shell=false.
		"&& unset OPENAI_BASE_URL OPENAI_API_KEY CODEX_API_KEY NODE_OPTIONS NODE_PATH PYTHONPATH PYTHONHOME PYTHONSTARTUP LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT BASH_ENV ENV ZDOTDIR SHELL",
		// `unset -f` is a special builtin, which a function .env defined
		// cannot shadow, so it restores the real utilities before the second
		// pass; `command -p` inside the guard defeats a PATH swap.
		"&& unset -f test command grep cut wc sha256sum printf codex",
		"&& "+codexAssetGuard(r, hooksEnabled, digests),
		"&& "+codexConfigGuard(r, digests),
	)
	if params.Debug != "" {
		// codex exec has no --debug flag. Its tracing goes to stderr, at
		// error level by default — a codex run is not silent there the way pi
		// is — and RUST_LOG raises it.
		parts = append(parts, `&& export RUST_LOG="${RUST_LOG:-info}"`)
	}

	prompt := DefaultAgentPrompt
	if params.Prompt != "" {
		prompt = params.Prompt
	}
	// The prompt goes in on stdin, never argv: it is attacker-influenced text
	// on a retry iteration (the validation loop injects the previous failure,
	// #1050/#6494) and argv is world-readable in the sandbox. `-` is codex's
	// explicit read-the-prompt-from-stdin sentinel; the pipe closes as soon as
	// printf is done, so the read cannot hang the way pi's does.
	if hooksEnabled {
		// Exported after .env so nothing the agent wrote there can move it,
		// and read by the adapter before every hook script it spawns.
		parts = append(parts, "&& export "+codexHookDigestsEnv+"="+
			shellQuote(codexHookDigestsValue(digests.HookScripts)))
		// The hook scripts' own configuration, re-asserted from what the
		// runner derived from the harness. appendHookEnv wrote the same values
		// into the workspace .env at bootstrap, and that file is
		// agent-writable: without this, iteration 1 could widen the SSRF
		// allowlist or clear TIRITH_REQUIRED for iteration 2 and the hooks
		// would read the agent's values. Reproduced before this was added.
		for _, pair := range digests.SecurityEnv {
			parts = append(parts, "&& export "+pair.Key+"="+shellQuote(pair.Value))
		}
	}
	parts = append(parts,
		"&& printf '%s' "+shellQuote(prompt)+` | "$`+codexBinaryVar+`"`,
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--dangerously-bypass-approvals-and-sandbox",
	)
	// Unmanaged hooks otherwise run only when their recorded trusted_hash
	// matches. fullsend's own SHA-256 guard above already vets the adapter
	// the handlers invoke, and the alternative — baking a managed hook
	// layer into /etc/codex at image build — would tie hook wiring to
	// image releases (ADR 0099).
	parts = append(parts, "--dangerously-bypass-hook-trust")
	parts = append(parts,
		"-C "+shellQuote(params.RepoDir),
		"--model "+shellQuote(model),
		"-c "+shellQuote("model_provider="+codexProviderID),
		"-c "+shellQuote("approval_policy=never"),
		"-c "+shellQuote("allow_login_shell=false"),
		// Codex applies this policy only to model tool subprocesses; native
		// children inherit it. It does not alter launcher/auth/hook resolution.
		`-c "shell_environment_policy.set.PATH=\"/sandbox/workspace/bin:/usr/local/go/bin:/sandbox/go/bin:$`+codexPathVar+`\""`,
		"-c "+shellQuote("sandbox_mode=danger-full-access"),
		// The endpoint and the credential command as SessionFlags too, so
		// even an edit that somehow satisfied the digest guard could not move
		// them. Verified against codex 0.152.1: a `-c` override of these two
		// beats the value in config.toml (a file naming an unreachable host
		// still reached api.openai.com). There is no such pin for project
		// trust — `-c projects={}` and a scalar `trust_level="untrusted"` were
		// both tried and neither overrides the file — so the untrusted entry
		// lives in config.toml, whose integrity is enforced by digest.
		"-c "+shellQuote(fmt.Sprintf("model_providers.%s.base_url=%q", codexProviderID, codexBaseURL)),
		"-c "+shellQuote(fmt.Sprintf("model_providers.%s.auth.command=%q", codexProviderID, r.codexAuthScriptPath())),
	)
	if effort != "" {
		parts = append(parts, "-c "+shellQuote("model_reasoning_effort="+effort))
	}
	parts = append(parts,
		"-o "+shellQuote(r.ConfigDir()+"/"+codexLastMessageFile),
		"-",
	)
	if params.Debug != "" {
		parts = append(parts, "2>>"+shellQuote(sandbox.SandboxWorkspace+"/"+codexDebugLogFile))
	}
	return strings.Join(parts, " ")
}

// Run executes one agent iteration and normalizes codex's `exec --json` stream
// into AgentEvents. codex exits 0 on a failed turn and on an interrupted one,
// so the stream's verdict overrides the exit code, as it does for pi.
func (r CodexRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, start time.Time, metrics *RunMetrics) (int, error) {
	clearCodexTranscriptIdentities(params.SandboxName)
	m, err := readCodexManifest(params.SandboxName, r.codexManifestPath())
	if err != nil {
		return -1, err
	}
	hooksEnabled := params.HooksSettingsPath != ""
	if hooksEnabled && (m.Hooks == nil || m.Hooks.Groups == nil) {
		return -1, fmt.Errorf(
			"security is enabled but the codex manifest at %s carries no hook plan (Bootstrap ran without the sandbox hook config, or the manifest was modified)",
			r.codexManifestPath())
	}

	digests, ok := lookupRunnerHeldDigests(params.SandboxName)
	if !ok {
		// The digests are runner-held because the sandbox has no trustworthy
		// place to keep them; the runner calls Bootstrap and Run in one
		// invocation, so a miss means the config was never written by this
		// process and the guards would have nothing to compare against.
		return -1, fmt.Errorf(
			"no runner-held config digests for sandbox %s: CodexRuntime.Run requires Bootstrap to have run in the same process (internal/cli/run.go does), because the expected digests cannot be read back from the agent-writable config directory",
			params.SandboxName)
	}
	// Both sides derive from the harness's SecurityEnabled() today — the
	// runner passes HooksSettingsPath, Bootstrap writes hooks.json — but
	// nothing asserted it, and a refactor that split them would silently drop
	// the hooks.json digest from the guard while the adapter still loaded.
	// nil, not empty: a harness may enable security and disable every
	// individual hook, which leaves HookFiles empty but the hooks path taken —
	// legal on pi too, whose check distinguishes a nil groups array from an
	// empty one for the same reason. A nil map means Bootstrap never ran the
	// hooks path at all, which contradicts the runner's signal.
	if hooksEnabled != (digests.HookScripts != nil) || digests.HooksJSON == "" {
		return -1, fmt.Errorf(
			"codex hook wiring is inconsistent: shared hooks enabled=%t, shared scripts recorded=%t, mandatory hooks.json recorded=%t; refusing to run",
			hooksEnabled, digests.HookScripts != nil, digests.HooksJSON != "")
	}
	// The same fallback chain NeedsOpenAIProvider decides from, so the launch
	// and the provider decision cannot disagree about which model this run
	// calls — a disagreement would either strand a frontmatter-pinned OpenAI
	// agent without a credential or attach one to a run that never uses it.
	//
	// The fallback is the runner-held copy of the agent definition's model,
	// not the manifest's: the manifest sits in the agent-writable config
	// directory and carries no digest, so reading the model from there would
	// let an agent move a validation retry onto a different model, and a
	// different cost tier, than the one the run was authorised for.
	modelID, err := translateCodexModel(EffectiveModel(params.Model, digests.AgentModel))
	if err != nil {
		return -1, err
	}
	effort, ok := codexEffortFor(params.Effort)
	if !ok {
		printer.StepWarn(fmt.Sprintf(
			"effort %q is not a codex reasoning effort; running at the model's default",
			sanitizeOutput(params.Effort)))
	}
	if len(params.FallbackModels) > 0 {
		// codex has no fallback chain. Say so rather than silently dropping it.
		printer.StepWarn(fmt.Sprintf(
			"fallback models %s are not supported on codex and are ignored",
			sanitizeOutput(strings.Join(params.FallbackModels, ","))))
	}

	cmd := r.OpenAIAuthSeed() + " && " + codexProtectedCommand(buildCodexRunCommand(params, modelID, effort, hooksEnabled, digests), digests)

	stdout, execCmd, cancel, err := sandbox.ExecStreamReader(ctx, params.SandboxName, cmd, params.Timeout, os.Stderr)
	if err != nil {
		return -1, err
	}
	defer cancel()

	var reader io.Reader = stdout
	if params.OutputPath != "" {
		f, ferr := os.Create(params.OutputPath)
		if ferr != nil {
			printer.StepWarn(fmt.Sprintf("Failed to create %s: ", params.OutputPath) + ferr.Error())
		} else {
			defer f.Close()
			// The stream keeps each command's raw aggregated_output even when
			// a hook blocked the result, and this file is uploaded as a run
			// artifact — so it is redacted on the way to disk while the parser
			// still sees the original (codex_redact.go).
			redacting := newCodexRedactingWriter(f)
			defer func() {
				if err := redacting.Flush(); err != nil {
					printer.StepWarn("Failed to flush " + params.OutputPath + ": " + err.Error())
				}
			}()
			reader = io.TeeReader(stdout, redacting)
		}
	}

	handler := params.OnEvent
	if handler == nil {
		renderer := NewEventRenderer(printer)
		handler = renderer.Handle
	}

	// The stream carries neither the CLI version nor the model, so the
	// InitEvent is emitted here from what the runner already knows and the
	// parser emits none.
	metrics.Model = modelID
	handler(InitEvent{Model: modelID, Version: m.CodexVersion})

	var lastResult *ResultEvent
	innerHandler := handler
	handler = func(evt AgentEvent) {
		applyCodexMetrics(metrics, evt)
		if e, ok := evt.(ResultEvent); ok {
			lastResult = &e
			// The terminal summary must include children. Retain the parent
			// result until the bounded rollout collection below is finished.
			return
		}
		innerHandler(evt)
	}

	children := map[string]bool{} // successful spawns, with observed completion
	closedChildren := map[string]bool{}
	rootID, parseErr := parseCodexStream(reader, handler, func(item codexCollabToolCallItem) {
		if (item.Tool == "spawn_agent" || item.Tool == "send_input") && item.Status == "completed" {
			for _, id := range item.ReceiverThreadIDs {
				children[id] = false
				closedChildren[id] = false
			}
		}
		for id, state := range item.AgentStates {
			// close_agent removes the thread, so waiting on or closing a closed
			// child again reports it not_found.
			if closedChildren[id] && state.Status == "not_found" {
				continue
			}
			if _, exists := children[id]; exists {
				switch state.Status {
				case "completed":
					children[id] = state.Message != ""
				case "pending_init", "running", "interrupted", "errored", "not_found":
					children[id] = false
				}
			}
		}
		if item.Tool == "close_agent" && item.Status == "completed" {
			for _, id := range item.ReceiverThreadIDs {
				if children[id] {
					closedChildren[id] = true
				}
			}
		}
	})
	rememberCodexTranscriptIdentities(params.SandboxName, rootID, children)
	if parseErr != nil {
		fmt.Fprintf(os.Stderr, "  progress parser: %v\n", sanitizeOutput(parseErr.Error()))
		cancel()
		io.Copy(io.Discard, reader)
	}

	waitErr := execCmd.Wait()
	exitCode := -1
	if execCmd.ProcessState != nil {
		exitCode = execCmd.ProcessState.ExitCode()
	}
	if waitErr != nil && execCmd.ProcessState == nil {
		return exitCode, fmt.Errorf("openshell exec failed: %w", waitErr)
	}
	// The exec stream reports root usage only. Collect persisted child records
	// even after cancellation, with a bounded cleanup context and no inference.
	usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	usageErr := r.collectCodexUsage(usageCtx, params.SandboxName, rootID, metrics, children, lastResult != nil && !lastResult.IsError)
	usageErr = errors.Join(usageErr, parseErr)
	usageCancel()
	if lastResult == nil || lastResult.Subtype == codexSubtypeIncomplete {
		// A killed exec leaves Codex writing until the runner terminates it:
		// drop the pinned digests so extraction publishes the final bytes.
		rememberCodexTranscriptIdentities(params.SandboxName, rootID, children)
	}
	for id := range children {
		if !closedChildren[id] {
			usageErr = errors.Join(usageErr, fmt.Errorf("child lifecycle: child %s was not closed after completion", id))
		}
	}
	if usageErr != nil {
		printer.StepWarn("Codex runtime evidence is incomplete: " + sanitizeOutput(usageErr.Error()))
		if exitCode == 0 {
			exitCode = 1
		}
	}
	if lastResult != nil {
		lastResult.InputTokens = metrics.InputTokens
		lastResult.OutputTokens = metrics.OutputTokens
		lastResult.ReasoningTokens = metrics.ReasoningTokens
		lastResult.CacheCreationInputTokens = metrics.CacheCreationInputTokens
		lastResult.CacheReadInputTokens = metrics.CacheReadInputTokens
		if usageErr != nil {
			lastResult.IsError = true
			lastResult.Subtype = codexSubtypeIncomplete
			lastResult.ErrorMessage = strings.TrimSpace(lastResult.ErrorMessage + "\nCodex runtime evidence is incomplete: " + usageErr.Error())
		}
		innerHandler(*lastResult)
	}
	if exitCode == codexHooksMissingExit {
		return exitCode, fmt.Errorf(
			"codex config, hook adapter or auth script missing or modified in %s; refusing to run (was Bootstrap run, or did the agent change it?)",
			r.ConfigDir())
	}
	if exitCode == codexConfigTamperedExit {
		return exitCode, fmt.Errorf(
			"codex config.toml in %s no longer pins the run-scoped provider endpoint, its auth command, or leaves the project untrusted; refusing to run because any of those can redirect or replace the runner's credential (did the agent write there between iterations?)",
			r.ConfigDir())
	}
	if exitCode == codexWriteProtectionExit {
		return exitCode, fmt.Errorf(
			"codex write protection failed in %s: Codex runs require Linux Landlock ABI 3+ (kernel 6.2+), and the runner-held config, role and hook files must match what Bootstrap wrote; refusing to run (see the sandbox stderr)",
			r.ConfigDir())
	}

	if exitCode == 0 && lastResult != nil && lastResult.IsError {
		msg := lastResult.ErrorMessage
		if msg == "" {
			msg = "stream ended without a completed turn (" + lastResult.Subtype + ")"
		}
		printer.StepWarn("codex exited 0 but the stream reports an error: " + sanitizeOutput(msg))
		exitCode = 1
	}
	if usageErr != nil {
		return exitCode, fmt.Errorf("%w: %w", ErrIncompleteEvidence, usageErr)
	}
	return exitCode, nil
}

func (r CodexRuntime) collectCodexUsage(ctx context.Context, sandboxName, rootID string, metrics *RunMetrics, expectedChildren map[string]bool, requireRootEvidence bool) error {
	if metrics == nil {
		return fmt.Errorf("codex usage requires run metrics")
	}
	// Keep the stream contribution until complete native evidence can replace
	// it. A failed start with no response usage keeps its original provider
	// error; successful runs and runs with incurred usage require a root.
	requireRootEvidence = requireRootEvidence || len(expectedChildren) != 0 ||
		metrics.InputTokens != 0 || metrics.OutputTokens != 0 || metrics.ReasoningTokens != 0 ||
		metrics.CacheReadInputTokens != 0 || metrics.CacheCreationInputTokens != 0
	metrics.CostUnavailable = true
	metrics.PerModelUsage = map[string]ModelUsage{metrics.Model: {
		CostUnavailable: true,
		InputTokens:     metrics.InputTokens, OutputTokens: metrics.OutputTokens,
		ReasoningTokens: metrics.ReasoningTokens, CacheReadInputTokens: metrics.CacheReadInputTokens,
		CacheCreationInputTokens: metrics.CacheCreationInputTokens, Requests: 1,
	}}
	if rootID == "" {
		if requireRootEvidence {
			return fmt.Errorf("codex usage is missing the root thread identity")
		}
		return nil
	}
	dir, err := os.MkdirTemp("", "fullsend-codex-usage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	paths, collectionErr := r.downloadCodexRollouts(ctx, sandboxName, dir)
	var rollouts []codexRolloutUsage
	var failures []error
	failures = append(failures, collectionErr)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		rollout, err := parseCodexRolloutUsage(f)
		f.Close()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		failures = append(failures, rememberCodexTranscriptFile(sandboxName, rollout.Meta, path))
		rollouts = append(rollouts, rollout)
	}
	observedChildren := map[string]bool{}
	for _, rollout := range rollouts {
		if rollout.Meta.ParentThreadID == rootID && rollout.Meta.SessionID == rootID {
			if _, dispatched := expectedChildren[rollout.Meta.ThreadID]; !dispatched {
				failures = append(failures, fmt.Errorf("child %s has no observed dispatch", rollout.Meta.ThreadID))
			}
			// A child without response usage still exists and must be accounted
			// for, even if its spawn record was absent from the live stream.
			observedChildren[rollout.Meta.ThreadID] = false
			for _, response := range rollout.Responses {
				if response.Record.ThreadID == rollout.Meta.ThreadID {
					observedChildren[rollout.Meta.ThreadID] = true
				}
			}
		}
	}
	for id, complete := range expectedChildren {
		if !complete {
			failures = append(failures, fmt.Errorf("child %s did not deliver a completed result", id))
		}
		if !observedChildren[id] {
			failures = append(failures, fmt.Errorf("child %s has no collected response usage", id))
		}
	}
	requireRootEvidence = requireRootEvidence || len(observedChildren) != 0
	// A later interrupted turn can incur usage after an earlier completed
	// stream snapshot. Persisted response deltas cover all observed turns;
	// replace the root contribution, never add the snapshot again.
	rootUsage, rootErr := foldCodexRootUsage(rootID, rollouts)
	failures = append(failures, rootErr)
	if rootErr == nil && len(rootUsage) == 0 && requireRootEvidence {
		failures = append(failures, fmt.Errorf("codex root has no collected response usage for thread %s", rootID))
	}
	if rootErr == nil && len(rootUsage) != 0 {
		rootMetrics := &RunMetrics{CostUnavailable: true}
		if err := addCodexUsage(rootMetrics, rootUsage); err != nil {
			failures = append(failures, err)
		} else {
			metrics.InputTokens, metrics.OutputTokens, metrics.ReasoningTokens = rootMetrics.InputTokens, rootMetrics.OutputTokens, rootMetrics.ReasoningTokens
			metrics.CacheReadInputTokens, metrics.CacheCreationInputTokens = rootMetrics.CacheReadInputTokens, rootMetrics.CacheCreationInputTokens
			metrics.PerModelUsage = rootMetrics.PerModelUsage
		}
	}
	children, err := foldCodexChildUsage(rootID, rollouts)
	failures = append(failures, err)
	if err == nil {
		failures = append(failures, addCodexUsage(metrics, children))
	}
	return errors.Join(failures...)
}

// ClearIterationArtifacts terminates processes the previous iteration left
// running (see killStrayProcesses), then removes its outputs, the rollout
// sessions and the debug log so transcripts and output files are
// per-iteration.
func (r CodexRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearCodexTranscriptIdentities(sandboxName)
	clearStrayProcesses(sandbox.Exec, sandboxName, os.Stderr, "the previous iteration")
	clearCmd := fmt.Sprintf("rm -rf %s/output/* %s/* %s %s",
		shellQuote(r.WorkspaceDir()),
		shellQuote(r.codexSessionsDir()),
		shellQuote(r.WorkspaceDir()+"/"+codexDebugLogFile),
		shellQuote(r.ConfigDir()+"/"+codexLastMessageFile))
	// The frozen CODEX_HOME directory cannot create a new top-level file.
	// Re-provision this writable state file before the next launch.
	clearCmd += " && touch " + shellQuote(r.ConfigDir()+"/"+codexLastMessageFile)
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}
