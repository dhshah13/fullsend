---
title: "72. Pre-script skip signalling via the pre-script output protocol"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - harness
  - workflows
  - forge
---

# 72. Pre-script skip signalling via the pre-script output protocol

Date: 2026-07-29

## Status

Accepted

## Context

An agent's harness `pre_script` runs inside `fullsend run`, immediately before
sandbox creation (the agents and the pipeline they run in are covered by the
[agent architecture](../problems/agent-architecture.md) and
[agent infrastructure](../problems/agent-infrastructure.md) problem docs).
`reusable-code.yml` and `reusable-fix.yml` additionally
invoked the same scripts inline, ahead of `fullsend run`, so a `skipped=`
step output could gate expensive setup (GCP credentials, bot identity,
agent-env prep). The result was two executions per run
([fullsend-ai/fullsend#4718](https://github.com/fullsend-ai/fullsend/issues/4718)) —
from two independently maintained copies, because the inline step can only
reach the scaffold copy embedded in fullsend while the harness resolves the
`fullsend-ai/agents` copy
([fullsend-ai/fullsend#5667](https://github.com/fullsend-ai/fullsend/issues/5667)) —
plus duplicated side effects from the existing-PR check.

A first fix ([#5013](https://github.com/fullsend-ai/fullsend/pull/5013),
[fullsend-ai/agents#175](https://github.com/fullsend-ai/agents/pull/175)) kept
both invocations and added skip flags so each call site skipped the other's
half. Review rejected that direction: it grows per-agent logic in workflow
YAML just as the generic CEL dispatch flow
([ADR 0061](0061-harness-cel-dispatch.md)) is removing it, it requires the
flags to stay synchronized across both copies in both repos, and the
flag-setting mechanism (workflow step `env:`) is GitHub-coupled — the GitLab
scaffold has no inline pre-check steps at all.

## Options

**Skip flags on dual invocations.** Keep both call sites; `{AGENT}_SKIP_{THING}`
env flags (naming per [ADR 0049](0049-agent-configuration-env-var-convention.md))
make each invocation skip the subset the other already performed. Smallest
immediate diff, but it entrenches the dual copies, deepens the divergence
between reusable workflows, and hard-codes the flag wiring in forge-specific
workflow YAML.

**Skip signalling inside `fullsend run`.** The pre-script runs exactly once,
inside `fullsend run`; a script that wants the run to stop writes a skip
signal to a file the CLI provides. The inline invocations — and with them the
scaffold script copies — are deleted.

**Absorb the gated setup into `fullsend run`.** Move GCP auth, token minting,
and bot identity into the CLI so no workflow step needs gating at all. The
logical endgame for platform-nativeness, but far larger in scope; it remains
compatible with the previous option as a later step.

## Decision

Adopt the second option, as the **pre-script output protocol**: `fullsend run`
exports `FULLSEND_PRESCRIPT_OUTPUT` (a file path) into the pre-script
environment; the script may write `skipped=true` plus an optional `reason`;
on skip, `fullsend run` reports a `skipped` status and exits successfully
before sandbox creation, relaying the outputs to the surrounding CI where one
is detected. The field-level contract — grammar, reserved keys, error
semantics, CI relay, version skew — is normative in
[`docs/normative/prescript-output/v1`](../normative/prescript-output/v1/README.md),
not here.

Reasons, in order of weight. The skip decision moves to the CLI layer, so
every forge inherits it without CI-side reimplementation and the reusable
workflows converge toward the generic `harness-run` shape
([ADR 0061](0061-harness-cel-dispatch.md)). The scripts become single-sourced
in `fullsend-ai/agents`, ending the dual-copy drift (#5667). No variables need
to pass between two invocations at all — the protocol file is the only
interface, owned end-to-end by the CLI — which dissolves the portability
problem rather than solving it. And version skew degrades to prior behavior in
both directions (same class of concern as
[ADR 0062](0062-dispatch-version-skew.md); the asymmetry is specified in the
normative doc).

The deferred third option (moving setup into `fullsend run`) is tracked in
[fullsend-ai/fullsend#5870](https://github.com/fullsend-ai/fullsend/issues/5870),
not decided here.

## Consequences

- The inline `Validate inputs` steps and their `skipped` gates are deleted
  ([#5739](https://github.com/fullsend-ai/fullsend/pull/5739)); a skipped run
  now pays the setup cost before `fullsend run` notices — accepted for the
  rare skip case.
- `pre-code.sh` / `pre-fix.sh` live only in `fullsend-ai/agents`; the fullsend
  scaffold no longer embeds them (#5667 resolved).
- Pre-scripts must guard on `FULLSEND_PRESCRIPT_OUTPUT` being unset: under an
  older CLI the check fails open (the agent runs) rather than crashing.
- CEL triggers ([ADR 0061](0061-harness-cel-dispatch.md)) still answer "does
  this event route to this agent?"; the protocol answers "having routed here,
  is there a reason not to proceed?" — a decision that can depend on forge API
  data, which CEL cannot express. Whether the pre-script performs the forge
  call itself or `fullsend run` performs it and passes the result in is not
  fixed by this decision.
- Since acceptance, pre-scripts run with only the credentials the harness
  supplies: `fullsend run` strips the OIDC vars that authenticate to the token
  mint from the pre-script environment
  ([#5832](https://github.com/fullsend-ai/fullsend/issues/5832),
  [#5837](https://github.com/fullsend-ai/fullsend/pull/5837)), enforcing the
  harness-as-sole-minter intent of
  [ADR 0073](0073-named-mint-privilege-levels.md) at this boundary. The open
  forge-call question above is therefore about which side holds the
  harness-minted token — scripts can no longer mint their own.
- The scripts' `gh` coupling is unchanged — a pre-existing forge gap tracked
  separately from this decision.
