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
check than the trust hash it replaces. The project is left **untrusted**, and approval policy,
sandbox mode and the model provider are additionally passed as `-c` overrides, which outrank every
config layer including the image's managed `/etc/codex/config.toml`.

## Consequences

- Codex has no Vertex path, so it gets **no default behaviour-CI coverage** until an OpenAI
  organization is mapped to the pool repositories; until then the evidence is unit tests, recorded
  stream fixtures and local smoke runs.
- Codex cannot rewrite a built-in tool's output — its PostToolUse accepts only `additionalContext`
  and `updatedMCPToolOutput` — so the sanitizer chain **detects and blocks** but does not redact,
  and the adapter warns the model that the output it is reading would have been redacted.
- Codex has **no hook-driven session halt**: `continue: false` is unsupported on PreToolUse and
  inert on PostToolUse. A canary hit therefore blocks, which on codex withholds the tool output
  entirely — stronger than Claude Code, where a block only appends a reason — but the run continues.
- Codex reports no cost, so `total_cost_usd` stays 0 for codex runs; token usage is reported.
- `apply_patch` covers both Claude's `Write` and `Edit`, and reaches the hook scripts as `Edit`, so
  an agent allowlisted only for `Write` is blocked by the (opt-in) tool-allowlist hook.
- The codex CLI pin now has to be re-checked on every bump against the hook payload shape,
  `auth.command` semantics and the JSONL event structs, since all three are load-bearing here.
