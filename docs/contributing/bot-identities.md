# Bot Identities

Fullsend agents authenticate as GitHub Apps; the table below also includes non-agent bots that appear in trusted-actor lists. Multiple agent roles may share a single app identity. The GitHub App login is derived from the `slug` field in each harness file (in the [`fullsend-ai/agents`](https://github.com/fullsend-ai/agents) repo).

| Agent role | GitHub App login | Notes |
|---|---|---|
| code | `fullsend-ai-coder[bot]` | Opens PRs from issues |
| fix | `fullsend-ai-coder[bot]` | Shares the coder app; pushes to existing PR branches |
| review | `fullsend-ai-review[bot]` | Posts review comments |
| triage | `fullsend-ai-triage[bot]` | Posts triage summaries on issues |
| retro | `fullsend-ai-retro[bot]` | Files retro issues, posts PR comments |
| prioritize | `fullsend-ai-prioritize[bot]` | Prioritizes issues |
| renovate | `renovate-fullsend[bot]` | Dependency updates (not a fullsend agent) |

When referencing bot identities in code (e.g., trusted actor lists, dispatch filters), always verify the login name against this table. Do not assume each agent role has a unique app identity — the fix agent reuses `fullsend-ai-coder[bot]`, not a separate `fullsend-ai-fix[bot]`.

**Shared vendor identity:** The default deployment model uses a shared, vendor-owned App (per ADR 0029/0059/0068). For adopting orgs other than `fullsend-ai`, the review bot's login is `fullsend-ai-review[bot]`, not `${ORG_NAME}-review[bot]`. Any code that gates on the review bot's identity must match **both** the org-specific form (`${ORG_NAME}-review[bot]`) and the shared vendor form (`fullsend-ai-review[bot]`). See #5550 for the bug this caused.

**REST vs. GraphQL login format:** the `[bot]` suffix above is the REST/App-slug form. GitHub's GraphQL API omits it — a bot author's `login` field comes back as `fullsend-ai-coder`, not `fullsend-ai-coder[bot]`, with `__typename: "Bot"`. Comparing a GraphQL-sourced login against a literal `"...[bot]"` string never matches (see #5575) — match on `__typename == "Bot"` plus the un-suffixed login instead.

**`gh pr view --json` format:** the `gh pr view --json author` CLI command uses a different schema than raw GraphQL — it exposes `.author.is_bot` (boolean) and `.author.login` (with an `app/` prefix, e.g. `app/fullsend-ai-coder`), but does **not** expose `__typename`. When using `gh pr view --json`, check `.author.is_bot == true` plus `.author.login` against the `app/`-prefixed name (see #5536).
