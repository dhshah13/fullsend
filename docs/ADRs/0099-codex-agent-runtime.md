---
title: "99. Codex as an agent runtime — custom provider, runner-seeded token, hook adapter"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - sandbox
  - security
---

# 99. Codex as an agent runtime — custom provider, runner-seeded token, hook adapter

Date: 2026-09-02

## Status

Accepted

## Context

Fullsend runs agents on `claude` and `pi` ([ADR 0091](0091-per-agent-runtime-model-effort.md)).
Adding [openai/codex](https://github.com/openai/codex) needs answers to two questions the runtime
contract leaves to each backend: how the agent gets an OpenAI credential without one entering the
sandbox, and how the runtime-neutral sandbox tool hooks ([ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md))
reach a CLI with its own hook protocol.

The credential half is constrained by [ADR 0092](0092-openai-wif-credential-delivery.md): the runner
exchanges a WIF assertion for a short-lived token, puts it in a run-scoped OpenShell provider, and
the sandbox only ever sees a gateway placeholder. Those tokens live minutes, and OpenShell 0.0.115
pins each placeholder to the credential generation it was issued for, so a running agent must be
able to pick up a *new* placeholder mid-run. Codex's built-in `openai` provider reads
`OPENAI_API_KEY` once at startup and built-in provider ids cannot be overridden, so it cannot.

Facts below were verified against `openai/codex` at tag `rust-v0.152.1`; the re-check list on a pin
bump is in [runtime-implementation.md](../contributing/runtime-implementation.md#codex-runtime-internals-6920).

## Options

**Built-in `openai` provider driven by the environment.** Simplest, and wrong: the process reads the
variable once, so it keeps using a placeholder that stops resolving at the first refresh.

**A managed hook layer baked into `/etc/codex/hooks.json` at image build.** Codex always runs managed
hooks without a trust prompt, which is attractive. Rejected for v1: it ties hook wiring to image
releases, so a hook fix would need a fullsend release and a fleet repin before it reached any run.

**Trusting the project directory.** Would let codex load the repo's own `.codex/` layer — including
repo-authored hooks and instructions — which is precisely the external-injection path the threat
model ranks highest.

## Decision

Codex is configured with a **custom model provider** (`fullsend-openai`, `wire_api = "responses"`)
whose `auth.command` is a runner-written script that prints the current placeholder from a
runner-owned token file under `CODEX_HOME`. Codex re-runs that command every
`refresh_interval_ms`, and the runner re-seeds the file after every credential refresh through the
runtime-neutral `OpenAICredentialSeeder` interface — the same follow-the-refresh shape pi gets from
re-reading `auth.json`, expressed in codex's own mechanism.

Sandbox hooks are wired through `$CODEX_HOME/hooks.json`, rendered from `security.HookPlan`, with
every handler invoking one embedded **adapter** that runs the shared hook scripts and translates the
wire protocol in both directions. The adapter is mandatory rather than optional because codex's
protocol differs from the scripts' in ways that fail *open* if forwarded verbatim: a hook that exits
anything but 0 or 2 is recorded as `Failed` and does not block, so the scripts' `exit 1` becomes
exit 2 with the reason on stderr; and only a synchronous handler can apply control effects, so the
wiring never carries an `async` key. Hooks are loaded with `--dangerously-bypass-hook-trust`,
justified by fullsend's own SHA-256 guard over the adapter and the auth script, which is a stronger
check than the trust hash it replaces. The project is left **untrusted** — enforced by a whole-file digest of the runner's
`config.toml`, because a single `projects."<repo>".trust_level = "trusted"` line there would load
the target repo's own `.codex/` layer and there is no `-c` override for trust. Approval policy,
sandbox mode, the model provider, the provider's `base_url` and its `auth.command` are additionally
passed as `-c` overrides, which outrank every config layer including the image's managed
`/etc/codex/config.toml`.

## Consequences

- Codex has no Vertex path, so it gets **no default behaviour-CI coverage** until an OpenAI
  organization is mapped to the pool repositories; until then the evidence is unit tests, recorded
  stream fixtures and local smoke runs.
- The sanitizer chain **detects and blocks but cannot redact** for the model, because codex allows no
  output rewrite for built-in tools; and codex has **no hook-driven session halt**, so a canary hit
  blocks the tool result — which on codex withholds it entirely, unlike Claude Code — but the run
  continues.
- Codex's own artifacts keep raw tool output where Claude Code's stream carries the post-hook
  result, so `output.jsonl` and the extracted rollout are filtered through the shared secret-pattern
  redactor: a credential is masked there, a canary is not withheld.
- The runner-owned files under `CODEX_HOME` are only as trustworthy as their digests' anchor, which
  is this binary for the embedded assets and the runner's own memory for the per-run files — so
  `Run` now requires `Bootstrap` to have run in the same process, and fails closed otherwise. The
  guard covers `config.toml`, `hooks.json` and **every hook script by name**, not just the adapter
  and the auth script, and the hooks directory must hold exactly those files. The adapter then
  **re-verifies each script against the digests fixed in the codex process's environment before
  every invocation**, closing the intra-iteration window a once-per-launch check leaves open — so
  codex is stricter about hook integrity than Claude Code and pi, where the scripts are never
  checked at all.
- Codex reports no cost (`total_cost_usd` stays 0), `apply_patch` reaches the hook scripts as `Edit`
  so an agent allowlisted only for `Write` is blocked by the opt-in allowlist hook, and the CLI pin
  must be re-checked on every bump against the hook payload shape, `auth.command` semantics and the
  JSONL event structs — the re-check table is in
  [runtime-implementation.md](../contributing/runtime-implementation.md#codex-runtime-internals-6920).
