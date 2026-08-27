---
title: "92. OpenAI WIF credential delivery — runner-side exchange, run-scoped provider"
status: Accepted
relates_to:
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - sandbox
  - runtime
---

# 92. OpenAI WIF credential delivery — runner-side exchange, run-scoped provider

Date: 2026-08-27

## Status

Accepted

## Context

The pi runtime (#6464) serves Claude, Grok and Gemini on Vertex, but no
OpenAI model (#6532). OpenAI now offers Workload Identity Federation: a
GitHub Actions job exchanges its OIDC JWT at
`POST https://auth.openai.com/oauth/token` for an opaque Bearer access
token that lives at most one hour.

The token is a plain header credential, which means fullsend can run GPT
models with **no OpenAI credential inside the sandbox at all** — ADR 0025
credential-delivery **tier 2**, one tier above today's Vertex path (which
puts a real GitHub OIDC token and an `external_account` file on the
sandbox filesystem).

### Alternatives considered

1. **`inference.local`** — at OpenShell 0.0.83 this is a cluster-global
   route: one provider *and one model* per gateway, applied to every
   sandbox. fullsend runs on shared gateways (the GitLab runner VM's
   persistent gateway, local dev, parallel eval cases), so concurrent
   runs with different models or service accounts would race. Rejected.

2. **Dynamic `token_grant`** — it is the only endpoint-scoped injection
   at 0.0.83, but the supervisor mints a SPIFFE JWT-SVID as an RFC 7523
   client assertion; OpenAI's exchange needs a GitHub-issued JWT plus
   `identity_provider_id`/`service_account_id` in a JSON body. Rejected.

3. **In-sandbox exchange** — `ACTIONS_ID_TOKEN_REQUEST_URL/_TOKEN` are
   runner-only by design (`oidcDenyKeys`, `internal/cli/run.go`, #5832 /
   ADR 0073), and pi's built-in `openai` provider has no WIF concept.
   Rejected.

## Decision

Runner-side exchange with a run-scoped OpenShell provider:

1. The runner exchanges the GitHub OIDC JWT for an OpenAI access token
   before sandbox creation (`internal/inference/openaiwif/`).
2. A run-scoped provider (`openai-<suffix>`) carries the token as a
   bare-key credential, never expanded through `os.ExpandEnv` (the
   opaque token may contain `$`). The provider has
   `--credential-expires-at` set (the token's own expiry, or a bounded
   lifetime for a static `OPENAI_API_KEY` from the runner environment)
   and is deleted in deferred cleanup regardless of `--keep-sandbox`;
   a provider whose expiry cannot be set is deleted immediately.
3. The `fullsend-openai` OpenShell profile scopes egress to
   `POST /v1/responses` on `api.openai.com` for `**/node` binaries.
4. The pi runtime gate passes `--api-key "$OPENAI_API_KEY"` so pi uses
   the placeholder. A config-dir integrity guard (exit 98 on
   `auth.json`/`models.json`) closes the redirect vector even with
   hooks disabled; it runs before the agent-writable `.env` is sourced
   and again after it behind `unset -f test`, so a sourced shell
   function cannot shadow the check.
5. The token value is redacted by exact match in run output and provider
   errors; `::add-mask::` is emitted on GitHub Actions;
   `FULLSEND_OPENAI_*` are in `oidcDenyKeys`.

### Accepted residual

Static placeholder injection is not yet restricted by profile endpoint
or binary metadata (OpenShell docs, `providers-v2.mdx` line 72). A
compromised agent could have the gateway resolve the placeholder on
another REST-inspected endpoint its policy allows — the cross-provider
risk ADR 0025 already documents. Accepted for the pilot because:

- The token scope is `api.model.request` only, not account-wide.
- The token lifetime is at most 1 hour.
- The provider is run-scoped with an expiry and deleted at run end.
- The fleet already runs with a real `GH_TOKEN` in the sandbox
  environment, so this is strictly less exposure than the status quo.

## Consequences

- GPT models on pi are usable via `openai/<model-id>` with no OpenAI
  credential inside the sandbox.
- A follow-up issue tracks expiry-driven refresh (re-exchange +
  `provider update` before `expires_in`).
- A follow-up tracks workflow changes to pass the three WIF IDs into
  the runner environment.
- Live end-to-end verification is gated on external access and run by
  a maintainer after merge.

## Related

- ADR 0025 — provider-based credential delivery (tier definitions)
- ADR 0073 — `oidcDenyKeys` (#5832)
- #6532 — GPT / Azure OpenAI / Bedrock providers for pi
- #6464 — pi runtime tracker
- #1952 — Anthropic WIF sibling design
