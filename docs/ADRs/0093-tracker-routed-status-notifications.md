---
title: "93. Route run-status notifications by event provenance"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - tracker
  - notifications
  - status-comments
  - jira
  - portability
---

# 93. Route run-status notifications by event provenance

Date: 2026-08-29

## Status

Accepted

Builds on:

- Forge abstraction: [ADR 0005](0005-forge-abstraction-layer.md)
- Conversation surface / domain split: [ADR 0086](0086-conversation-surface-for-agent-participation.md)

## Context

Agent run-status notifications (start comments, completion comments, emoji
reactions, and orphan reconciliation) were coupled to `forge.Client` with
`owner/repo/number` addressing. This works when the triggering work item
and the code repository live on the same forge, but breaks when an external
issue tracker drives the work.

The concrete failure: when a Jira issue triggers a code-agent run, the
Jira issue number (e.g. 6964193) is passed to `forge.Client` as a GitHub
issue number. No such GitHub issue exists, producing:

```
Failed to post start status: posting start comment:
  create issue comment on #6964193: github api: 404 Not Found
```

The repository already has a tracker-neutral comment interface
(`tracker.Client`) with adapters for GitHub/GitLab (via `forge.Client`)
and Jira. ADR 0086 established the domain split: `forge.Client` for
git-hosting, `tracker.Client` for issue content, `conversation.Client`
for chat. Status notifications are issue content, not git-hosting
operations, so they belong on `tracker.Client`.

## Options

### A. Special-case Jira in the notifier

Add Jira-specific branching to `statuscomment.Notifier` alongside the
existing `forge.Client` calls. Fast to implement but duplicates the
tracker abstraction and grows worse with each new tracker backend.

### B. Route status notifications through `tracker.Client` (chosen)

Replace `forge.Client` with `tracker.Client` in the status-notification
path. Comments route to whichever tracker originated the run. Reactions
become an optional capability (`tracker.Reactor` interface) since not all
trackers support emoji reactions.

### C. Post status to both the tracker and the forge

Dual-write so status appears on both the Jira issue and a corresponding
GitHub issue. Requires a matching GitHub issue to exist (or be created),
adds complexity, and doubles API calls for every status update.

## Decision

Adopt **Option B**.

### Interface changes

1. **`tracker.Client`** gains `DeleteComment` for cleaning up transient
   start comments when completion is suppressed.

2. **`tracker.Reactor`** is a new optional interface:

   ```go
   type Reactor interface {
       AddIssueReaction(ctx, project, number, content) (id, error)
       DeleteIssueReaction(ctx, project, number, reactionID) error
       AddCommentReaction(ctx, project, number, commentID, content) (id, error)
       DeleteCommentReaction(ctx, project, number, commentID, reactionID) error
   }
   ```

   `tracker.ForgeClient` implements `Reactor` (GitHub and GitLab support
   emoji reactions). `tracker.JiraClient` does not (Jira has no
   equivalent). Consumers type-assert to `Reactor` and silently skip
   reaction operations when the tracker does not support them.

### Notifier changes

- `statuscomment.Notifier` accepts `tracker.Client` instead of
  `forge.Client`.
- Addressing changes from `(owner, repo string, number int)` to
  `(project string, number int)` to match `tracker.Client`'s
  project-keyed model.
- `ClientFactory` returns `tracker.Client` instead of `forge.Client`.
- `SetTriggerCommentID` accepts `string` (tracker comment IDs are
  strings for JSON round-tripping safety and non-numeric ID support).

### ReconcileOrphaned changes

- Accepts `tracker.Client` and `(project, number)` instead of
  `forge.Client` and `(owner, repo, number)`.
- The `reconcile-status` CLI command wraps its forge client in
  `tracker.NewForgeClient()` before calling `ReconcileOrphaned`.

### Call-site wiring

Existing forge-based callers (`setupStatusNotifierGitHub`,
`setupStatusNotifierGitLab`, `reconcile-status` command) construct
`tracker.NewForgeClient(forgeClient)` and pass the result. The
`ClientFactory` in the GitHub path returns
`tracker.NewForgeClient(gh.New(mintedToken))` so each token refresh
produces a tracker-wrapped client.

For Jira-triggered runs, the caller would construct a
`tracker.JiraClient` instead. The plumbing for reading source-system
from the normalized event is a follow-on; the interface is ready for it.

## Consequences

- Status comments for Jira-triggered runs can now be routed to Jira
  instead of producing a 404 on a non-existent GitHub issue.
- Reactions are silently skipped for trackers that do not support them
  (e.g. Jira), rather than failing or falling back to an unrelated forge
  issue.
- The `statuscomment` package no longer imports `internal/forge`,
  depending only on `internal/tracker` and `internal/config`.
- Adding a new tracker backend (e.g. Linear, Azure DevOps) requires only
  implementing `tracker.Client` (and optionally `tracker.Reactor`);
  status notifications work automatically.
- The orphan reconciler uses the same tracker-aware routing, so
  interrupted runs on Jira issues are also finalized correctly.
- `jira.LiveClient` gains a `DeleteComment` method to satisfy the
  extended `tracker.Client` interface.
