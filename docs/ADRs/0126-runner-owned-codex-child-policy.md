---
title: "126. Runner-owned policy for native Codex children"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - sub-agents
  - security
---

# 126. Runner-owned policy for native Codex children

Date: 2026-09-28

## Status

Accepted

## Context

Review and retrospective workflows need independent child contexts, selected
personas, bounded execution and attributable usage
([agent architecture](../problems/agent-architecture.md)). Codex provides native
children, but reads role configuration again when each child starts. Checking a
file only before the parent starts does not protect subsequent child dispatch.

[ADR 0104](0104-per-persona-model-resolution.md) makes model selection a runner
concern. Its Claude aliases and persona tool lists cannot be applied directly
to Codex. [ADR 0100](0100-codex-sandbox-hooks.md) translates the shared security
hooks; their wiring must also remain intact when native children load it.

## Decision

The runner provisions and protects the policy for native Codex children.
For an agent with the Agent tool, it registers skill personas plus generic and
Explore roles; other agents get no roles, as on Claude Code and pi. For Codex, model
precedence is an explicit persona mapping, `FULLSEND_CODEX_SUBAGENT_MODEL`,
`subagents.default`, then `gpt-5.6-luna`. Claude frontmatter models and tool
lists are reported as documentation only. Invalid or unavailable explicit
models never fall back to another model.

Before sourcing the sandbox environment or starting Codex, the runner applies
an inherited Linux Landlock write restriction. Role files, `CODEX_HOME`
configuration, hooks and their directory entries remain protected; exact
native state paths remain writable. The launcher closes inherited writable
descriptors and verifies authority files against runner-held hashes after
applying the restriction. A missing capability or changed policy fails the run.

Native configuration bounds open child IDs to four, including completed children
that have not been closed. A mandatory dispatch
hook rejects grandchildren, context inheritance, model overrides and resuming
any child, including when optional security scanning is disabled. Native
resume can replace a child's role policy with the parent's; a new task must
use a fresh spawn. Shared security hooks apply to parent and child tools.
Model metadata determines the native collaboration API: validation must cover
the selected model's lifecycle, not infer it from the CLI version alone.
The dispatch guard accepts only V1's explicit `fork_context: false` contract
and rejects V2-shaped arguments. A child model override does not select the
parent's collaboration API; a model-name allowlist is not a substitute for
enforcing the actual dispatch contract.

Native review and retro ship only as a paired runtime and companion release;
existing deployments keep their prior pins until they adopt the validated pair.

## Consequences

- Persona selection remains a workflow decision; models and dispatch constraints remain runner decisions.
- Codex requires a system Python outside the agent's write roots and Linux Landlock ABI 3 or newer inside its OpenShell sandbox, including for runs without children. Top-level `$HOME` entries that Bootstrap does not pre-create cannot be created during a run.
- Native state paths, role loading, hook inheritance and collaboration APIs require revalidation on CLI or model changes.
- Parent and child usage and transcripts are collected by native thread identity; incomplete evidence fails visibly, and unreported dollar cost remains unavailable.
- These restrictions protect the launched native hierarchy; tool hooks remain defense in depth within the outer sandbox, not a prohibition on every interpreter an agent can invoke.
