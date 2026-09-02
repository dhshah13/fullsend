# Codex

[Codex](https://github.com/openai/codex) is fullsend's third agent runtime, opt-in per org, repo or
agent. It runs **OpenAI models only**, through the same sandbox, egress policy and secretless
credential path pi uses for GPT — the runner holds the credential, the sandbox never sees it.

```bash
fullsend run triage --runtime codex --model openai/gpt-5.6-luna
```

Selecting it, and how it compares to the other runtimes, is in [Agent runtimes](../runtimes.md).
This page is what changes once you are on it.

## Models

**Codex takes an OpenAI model id** — `openai/<id>` or the bare id — and nothing else. It serves the
OpenAI Responses API, so there is no Claude, Gemini or Grok on codex.

**The Claude aliases do not apply.** `opus`, `sonnet`, `haiku` and `fable` name Anthropic models, so
codex refuses them rather than picking a GPT model on your behalf:

```
codex serves OpenAI models only; "opus" names a Claude model — use openai/<id>
```

<!-- TODO(D): replace the message above with the exact refusal text the runtime prints (the alias
     case, which is the one a repo on fleet harnesses actually hits). Quote it; do not paraphrase. -->

That matters because the fleet harnesses ship `model: opus`. You do not need to edit them — there
are two places to name a model for a repo on codex.

**A default for every agent in the repo,** set on the runner:

```bash
FULLSEND_CODEX_MODEL=openai/gpt-5.6-luna
```

It is read only when codex is the runtime actually selected, and it sits below `--model` and
`FULLSEND_MODEL` in the [usual precedence](../runtimes.md#selecting-a-runtime-and-model) — so it is a
default, not an override. When it decides the model, `metrics.json` records
`override_source: FULLSEND_CODEX_MODEL` and the plan block names it, the same way
`FULLSEND_PI_MODEL` does on pi.

**A model for one agent,** on its `agents:` entry in `.fullsend/config.yaml`, which also outranks the
harness:

```yaml
agents:
  - name: triage
    runtime: codex
    model: openai/gpt-5.6-luna
```

Effort maps onto codex's own reasoning levels:

| `--effort` | Codex `model_reasoning_effort` |
|---|---|
| `low` | `low` |
| `medium` | `medium` |
| `high` | `high` |
| `xhigh` | `xhigh` |
| `max` | `xhigh` |

Codex has no equivalent of a fallback model chain: `FULLSEND_FALLBACK_MODELS` is ignored with a
warning, as it is on pi.

## At a glance

| | |
|---|---|
| Credentials | A runner-exchanged OpenAI WIF token in CI, or your `OPENAI_API_KEY` on the runner locally — never in the sandbox. Codex reads a placeholder from a runner-owned token file and re-reads it when the credential is refreshed ([ADR 0092](../ADRs/0092-openai-wif-credential-delivery.md)) |
| Unattended | Approvals off; codex's own sandbox off, because OpenShell is the boundary. A missing credential exits before the agent starts |
| Artifacts | `output.jsonl` (the `codex exec --json` stream), `transcripts/<agent>-<rollout>.jsonl`, `last-message.txt`, `metrics.json` with `runtime: codex`, plus `codex-debug.log` with `--debug` |
| Extra knobs | `FULLSEND_CODEX_MODEL` (the runner-side model default for codex runs; see [Models](#models)) |
| Not supported | Sub-agents, `plugins:`, fallback chains, non-OpenAI providers |

Cost is **not** in `metrics.json` on codex: the `codex exec --json` stream carries no cost field, so
the value stays `0`. Token counts are recorded normally.

## Running it locally

Complete [Running agents locally](../guides/user/running-agents-locally.md) first — the CLI,
OpenShell, credentials and the fleet clone are the same. Add `OPENAI_API_KEY` to an env file as that
guide's [OpenAI section](../guides/user/running-agents-locally.md#get-an-openai-key-gpt-on-pi-or-codex)
describes, then add `--runtime codex` to any example on it:

```bash
fullsend run triage \
  --fullsend-dir /tmp/fullsend-agents/ \
  --target-repo /tmp/target-repo/ \
  --env-file fullsend-gcp.env \
  --env-file fullsend-openai.env \
  --env-file fullsend-triage.env \
  --runtime codex \
  --model openai/gpt-5.6-luna
```

The plan block confirms the selection — overridden values carry their source, harness defaults print
bare — and `metrics.json` records the same (`runtime`, `runtime_source`, `requested_model`,
`override_source`):

```
    Model: gpt-5.6-luna
    Effort: high
    Runtime: codex (from --runtime flag)
...
runtime: selected "codex" from --runtime flag
```

<!-- TODO(D): replace the plan block above with the real transcript from PR D's local smoke run,
     including the "→ Agent: … (v…)" and "→ Result: …" lines, once that evidence exists. -->

To keep an agent on codex (or off it) without passing flags every time, set `runtime:`/`model:` on
its `agents:` entry in `config.yaml` — see [per-agent
settings](../runtimes.md#per-agent-runtime-model-and-effort).

What a local codex run needs, beyond the guide:

- **A sandbox image that includes codex** — `ghcr.io/fullsend-ai/fullsend-sandbox` built with
  `CODEX_VERSION`. A stale image fails preflight; `podman pull
  ghcr.io/fullsend-ai/fullsend-sandbox:latest` fixes it.
- **A harness that declares the provider and a policy** — `providers: [openai]` and
  `policy: policies/base.yaml`. The fleet's agents already carry both; a custom harness needs the
  policy because the sandbox image's default policy leaves an uninspected route to `api.openai.com`,
  which the gateway refuses to carry the credential over.
- **Debugging** — `--debug='*'` (the `=` is required); sandbox-side failures land in
  `codex-debug.log` inside the run directory, next to the transcripts, not in the runner's output.

<!-- TODO(D): add the fullsend version floor (the first release carrying the codex runtime), the
     exact preflight error text for a stale image, and the platform line ("verified end to end on
     macOS Apple Silicon and Fedora with rootless Podman") once PR D's smoke runs are recorded. -->

## Behaviour differences worth knowing

- **No permission system.** Codex's posture, like pi's, is "run in a container". The sandbox, its
  egress policy and credential placeholders are the boundary
  ([ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md)); fullsend's hook adapter is
  defense-in-depth on top.
- **Reads `AGENTS.md` natively** — no `CLAUDE.md` bridge is injected.
- **The repository's own `.codex/` is never loaded.** Codex reads a project config layer only for a
  directory it has been told to trust, and fullsend does not trust the cloned repo — so a target
  repo cannot change how the agent runs.
- **Two tools, not a menu.** Codex works through a shell and `apply_patch`, so a harness `tools:`
  list has no native allowlist to map onto. Enforcement is the same pre-tool allowlist hook the
  other runtimes use, plus a warning naming the entries codex has no tool for.
- **PostToolUse hooks detect and block, but do not rewrite.** Codex accepts a block decision from a
  post-tool hook, but not replacement output for a built-in tool. A sanitizer that would have
  redacted something reports that fact to the model instead of silently changing the output.
- **Skills** come from the runner-owned `CODEX_HOME/skills/`; the repository's `.agents/skills` is
  discovered by codex natively.
- **No cost in metrics** — see [At a glance](#at-a-glance).

## Not yet exercised

`runtime: codex` is selectable, but there is **no fleet lifecycle run** on it yet, and no live run on
the OpenAI Workload Identity path: that is gated on an OpenAI organization being mapped to the pool
repositories, exactly as it is for pi. `e2e/behaviour/features/runtime/codex-openai.feature` stays
gated on the `runtime-codex-openai` capability until then, so codex has no default behaviour-test
coverage. Pilot on a disposable repo with `triage`/`prioritize` before `code`/`fix`.

Sub-agent rosters are not available: codex has no sub-agent tool in this version, so `review` and
`retro` — which rely on a parallel persona roster — should stay on Claude Code.

<!-- TODO(D): state exactly what was run and where, from PR D's smoke evidence (OpenShell and codex
     versions, model, which artifacts were confirmed), replacing the general wording above. -->

## Troubleshooting

**Preflight fails on the codex binary.** The sandbox image predates the `CODEX_VERSION` pin. Pull a
current `ghcr.io/fullsend-ai/fullsend-sandbox` image.

**`provider auth command ... exited`.** Codex could not read the credential. The runner-owned token
file is missing, or it does not hold a gateway placeholder — check that the harness declares
`providers: [openai]` and that `OPENAI_API_KEY` reached the **runner**, not the sandbox.

**The run stops before the agent starts, naming `api.openai.com`.** The effective sandbox policy
admits `api.openai.com:443` without protocol inspection, so the gateway refuses to carry the
credential over it. Add `policy: policies/base.yaml` to the harness.

**`codex serves OpenAI models only`.** The model carries another provider's prefix. Use
`openai/<id>` or a bare id.

**The run stops with a hook guard error.** The runner-written hook wiring under `CODEX_HOME` was
missing or modified. This is fail-closed by design — codex will not run unhooked.

**`--debug "..."` fails with `accepts 1 arg(s)`.** `--debug` takes an optional value: write
`--debug='*'` (with `=`).

**The agent fails with nothing in the terminal.** Sandbox-side codex failures land in
`codex-debug.log` inside the run directory, next to the transcripts; kept sandboxes must be removed
manually (`openshell sandbox delete <name>`).

**The run used Claude instead of codex.** The runtime falls back to `claude` when neither the
config's `runtime:` (repo-wide or on the agent's `agents:` entry) nor `--runtime`/`FULLSEND_RUNTIME`
selects codex; the plan block's `Runtime:` line and stderr's `runtime: selected ...` show which one
ran and why.

<!-- TODO(D): verify each symptom above against PR D's smoke run and replace the paraphrases with
     the exact message text the runner prints. -->

## See also

- [Agent runtimes](../runtimes.md) — choosing and selecting a runtime
- [Running agents locally](../guides/user/running-agents-locally.md) — the local-run flow that [Running it locally](#running-it-locally) builds on
- [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md) — the CI credential path

<!-- TODO(D): add the "codex runtime internals" link into
     ../contributing/runtime-implementation.md and the ADR 0099 link once PR D lands them; the
     markdown-link linter rejects them while the targets do not exist. -->
