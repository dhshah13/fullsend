---
title: "86. Conversation surface for agent participation"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - discussions
  - dispatch
  - conversation
  - thread
  - tracker
  - portability
  - slash-commands
---

# 86. Conversation surface for agent participation

Date: 2026-08-11

## Status

Accepted

Builds on:

- Forge abstraction: [ADR 0005](0005-forge-abstraction-layer.md)
- Credential isolation / host API: [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md),
  [ADR 0046](0046-host-side-api-server-design.md)
- Authorization / registration / dispatch: [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md),
  [ADR 0058](0058-agent-registration.md),
  [ADR 0061](0061-harness-cel-dispatch.md),
  [ADR 0063](0063-polling-based-work-discovery.md)
- Entity-context separation: [ADR 0076](0076-slash-command-entity-context-separation.md)

Aligns with the `tracker.Client` split
([#5988](https://github.com/fullsend-ai/fullsend/issues/5988)): domain
interfaces stay narrow; `forge.Client` remains git-hosting only.

## Context

Agents already act on forge **work items** and **change proposals** via slash
commands and host/post-script comments. GitHub Discussions (and later Slack,
Discord, Matrix, GitLab discussions) are a different surface: community chat
that must not be shoehorned into `work_item` without breaking entity-context
rules ([ADR 0076](0076-slash-command-entity-context-separation.md)).

`forge.Client` is scoped to git-hosting. Issue content for non-forge backends
(Jira) already moves to `tracker.Client`. Slack and peers are not forges —
chat read/write must not grow `forge.Client`.

Slack's API already names the container a **conversation** (channel/DM) and
replies under a parent a **thread**. Fullsend should use those words so
adapters map by name, not by overloaded metaphors. GitHub Discussions fit the
same shape: a Discussion is a conversation; a top-level comment plus its flat
replies is a thread (GitHub allows only one reply nesting level). Native
webhook payloads associate replies via a parent pointer on the message
(`comment.parent_id` on `discussion_comment`), not a separate thread object.

## Options

### A. Special-case GitHub Discussions in workflows

Fastest for GitHub; GitHub-centric and duplicates auth/writeback.

### B. Always-on chat bot outside the execution stack

Low latency; bypasses unidirectional control, mint/OIDC identity, and harness
registration ([ADR 0016](0016-unidirectional-control-flow.md)).

### C. Extend `forge.Client` with Conversation/Message methods

Pulls non-forge chat into the forge package and fights the `tracker.Client`
split.

### D. Portable Conversation surface via `conversation.Client` (chosen)

Parallel to `tracker.Client`: narrow domain interface + dispatch/event reuse,
with per-backend adapters.

## Decision

Adopt **Option D**.

### Domain model

| Relation | Cardinality | Notes |
|----------|-------------|--------|
| Category → Conversation | **1:M** | Exclusive partition (GitHub Discussion category; Slack channel **section** when mapped). |
| Conversation → Thread | **1:M** | A thread is the top-level message plus replies that name it as parent. |
| Thread → Message | **1:M** | Flat within the thread on GitHub (reply-to-reply nesting rejected). |
| Conversation ↔ Label | **M:M** | Triage tags on the conversation when the backend supports them. |
| Message / Thread → Category / Label | **none** | Inherit routing context from the parent conversation. |

**Backend mapping**

| Fullsend | Slack API / UI | GitHub Discussions |
|----------|----------------|--------------------|
| Category | Channel section (optional grouping) | Discussion category |
| Conversation | Conversation (channel / DM) | Discussion |
| Thread | Thread (`thread_ts`) | Top-level comment + flat replies |
| Message | Message | Comment or reply |

**Message identity and threading in events.** Comment transitions carry the
message itself: `transition.comment.id` and `transition.comment.parent_id`
(both required for conversation comment events). `parent_id` is always the
thread-root message id — the safe target for a reply in that thread. For a
top-level / thread-root message, adapters set `parent_id` equal to `id`. For a
reply, `parent_id` is the root (GitHub Actions `discussion_comment` uses
`null` parent on the root and a numeric parent on replies — normalize root to
`parent_id == id`; Slack maps `thread_ts`, using the message `ts` when the
message starts the thread). Threads are not a separate `entity.kind`.

**Category ≠ label.** Categories are a single exclusive partition with optional
*format* hints (open discussion, Q&A, announcement, poll). Labels are additive
tags on the conversation. Adapters must not encode the category name as a
synthetic label.

**Permissions.** Platform authorization remains
[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md). Category
and message/parent ids are **routing metadata** for harness CEL, not a second
auth system.

GitHub's public GraphQL `DiscussionCategory` exposes `isAnswerable` but no
announcement/poll/discussion format field. Adapters MUST map
`isAnswerable == true` to `format: question_answer` and SHOULD omit `format`
otherwise unless they document an explicit heuristic. UI-only create
restrictions are not queryable via that API and MUST NOT be treated as
enforceable from `format` alone.

### Architecture seams

1. **`conversation.Client`:** `internal/conversation` with Conversation,
   Thread, and Message APIs (category metadata on Conversation). GitHub
   Discussions first; Slack/peers implement directly. **Not** on
   `forge.Client` or `tracker.Client`.
2. **`NormalizedEvent`:** `entity.kind: conversation`. Require
   `state.conversation.category` (`name` required; `id`/`slug`/`format`
   optional). On comment transitions, require `transition.comment.id` and
   `transition.comment.parent_id` (`parent_id == id` for thread-root messages).
   Reuse `state.labels` for conversation labels only. Ingress covers
   conversation lifecycle events and message events on conversations. See
   [normative v1](../normative/normalized-event/v1/).
3. **Ingress / egress / identity / security:** shim + dispatch drivers;
   host-mediated writeback via `conversation.Client`; least-privilege
   Discussions/chat scopes; conversation and message bodies untrusted.
   Entity-context separation keeps code-mutating slash commands off
   conversations ([ADR 0076](0076-slash-command-entity-context-separation.md)).

## Consequences

- Harnesses can route with CEL on `state.conversation.category`,
  `transition.comment.id` / `parent_id`, and `state.labels`.
- Slack maps channel→conversation, section→category; `parent_id` is always the
  reply target (Slack `thread_ts` / message `ts`; GitHub normalized parent).
- `entity.id` remains numeric for forge-native conversations (e.g. Discussion
  number); non-numeric backends (Slack/Discord/Matrix) need a follow-on carrier
  (e.g. opaque `entity.key` or string id) before those adapters.
- Domain split: `forge.Client` / `tracker.Client` / `conversation.Client`.
- Linking a conversation or thread to a work item for `/fs-code` remains a
  follow-on.
