---
title: "78. Simplified mint authorization policy"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - mint
  - identity
  - oidc
  - least-privilege
---

# 78. Simplified mint authorization policy

Date: 2026-08-03

## Status

Accepted

Supersedes the `PER_ORG_FOREIGN_COMPAT` mechanism in [ADR 0077](0077-mint-repos-scope-hardening.md).

## Context

The mint's OIDC token authorization had grown complex: each verifier backend (JWKS, STS) independently implemented the same policy decisions (org-allowed check, per-repo bypass), and the `repos` scope rules depended on a separate `PER_ORG_FOREIGN_COMPAT` feature flag that operators had to configure independently of enrollment. The flag existed because [ADR 0077](0077-mint-repos-scope-hardening.md) hardened same-org repos scope to requesting-repo-only by default, but org-mode dispatch patterns (`.fullsend` callers minting across enrolled repos) still needed broader shapes.

This created three pain points:

1. **Duplicated policy in verifiers.** The per-repo bypass and org-allowed checks were copy-pasted into `JWKSVerifier.Verify()` and `STSVerifier.prevalidate()`, requiring synchronized edits.
2. **Separate knobs for related concerns.** Operators had to reason about both `ALLOWED_ORGS` membership and `PER_ORG_FOREIGN_COMPAT` to understand which repos shapes a caller could use.
3. **Public mode fragmentation.** Public mode (`ALLOWED_ORGS=*`) and per-repo enrollment (`PER_REPO_WIF_REPOS`) were orthogonal but semantically overlapping.

## Decision

### Unified authorization policy

Extract the common authorization decision into a single `AuthorizeToken` function called by both verifiers. The policy is:

1. **`repository_owner` must be non-empty** (defense-in-depth; GitHub OIDC always populates it, but the check is explicit).
2. **Per-repo mode** (`IsPerRepoMode`): if the caller's `repository` claim appears in `PER_REPO_WIF_REPOS` (or `PER_REPO_WIF_REPOS` contains `*`), the caller is authorized without requiring `repository_owner` in `ALLOWED_ORGS`. Per-repo callers can only mint tokens scoped to their own repository.
3. **Per-org mode**: otherwise, `repository_owner` must be in `ALLOWED_ORGS`. Per-org callers get org-mode repos shapes (`.fullsend` callers: any non-empty validated list; other enrolled callers: `[.fullsend]` or `{self, .fullsend}`).

### Public mint mode via PER_REPO_WIF_REPOS

Public mint mode is now expressed as `*` in `PER_REPO_WIF_REPOS` rather than `*` in `ALLOWED_ORGS`. This means every caller gets per-repo treatment: authorized without org-allow checks, but restricted to requesting-repo-only scope. The `ValidateWorkflowRef` public-mode path (upstream-only workflow provenance) keys off `IsPublicMintRepos(perRepoWIFRepos)`.

### Drop PER_ORG_FOREIGN_COMPAT

The `PER_ORG_FOREIGN_COMPAT` environment variable, Worker config field, status API field, and CLI display are removed. Org-mode repos shapes are now inherent to per-org callers: if a caller's org is in `ALLOWED_ORGS` and the caller's repo is not in `PER_REPO_WIF_REPOS`, the caller automatically gets the broader repos shapes previously gated by the compat flag.

## Consequences

- Verifier backends are now pure authenticators: they parse and verify the token, then return claims. The handler calls `AuthorizeToken` + `ValidateWorkflowRef` after authentication succeeds. Policy changes require editing one function, in one place.
- Operators no longer need to set `PER_ORG_FOREIGN_COMPAT` separately; per-org functionality follows from `ALLOWED_ORGS` membership.
- The `GET /v1/status` response no longer includes `per_org_foreign_compat`. Clients parsing this field should ignore its absence.
- Existing deployments with `PER_ORG_FOREIGN_COMPAT=true` and orgs in `ALLOWED_ORGS` see no behavior change: their callers were already both org-enrolled and had compat enabled; now compat is implicit for org-enrolled callers.
- Existing deployments with `PER_ORG_FOREIGN_COMPAT=false` (or unset) where callers are only in `ALLOWED_ORGS` will gain org-mode repos shapes they did not previously have. Operators who relied on the flag being off to enforce strict requesting-repo-only scope for org callers should migrate those callers to `PER_REPO_WIF_REPOS` entries.
- `ValidateWorkflowRef` now keys public mint mode off `IsPublicMintRepos(perRepoWIFRepos)` instead of `IsPublicMint(allowedOrgs)`. Existing `ALLOWED_ORGS=*` deployments without `PER_REPO_WIF_REPOS=*` will see workflow ref validation change — they should add `PER_REPO_WIF_REPOS=*` to maintain equivalent behavior, or explicitly accept the upstream-only restriction.

## Migration

Operators upgrading to this version should review:

1. **Public mint deployments** (`ALLOWED_ORGS=*`): Add `PER_REPO_WIF_REPOS=*` to your environment to maintain public mint behavior. Without it, `ValidateWorkflowRef` will not enter public-mode (upstream-only) validation, and callers using non-upstream workflow refs will be rejected.

2. **`PER_ORG_FOREIGN_COMPAT` removal**: Remove `PER_ORG_FOREIGN_COMPAT` from your environment. It is no longer read. Org-mode repos shapes are now implicit for `ALLOWED_ORGS` members.

3. **Historical per-repo enrollments**: Earlier `mint enroll repo` commands added orgs to both `PER_REPO_WIF_REPOS` and `ALLOWED_ORGS`. The extra `ALLOWED_ORGS` entry is benign (per-repo takes precedence for those callers), but you can clean up stale entries if the org has no other per-org callers.

4. **No configuration changes needed** for standard per-org deployments (orgs in `ALLOWED_ORGS`, no per-repo enrollments, no public mode). These deployments see no behavior change.
