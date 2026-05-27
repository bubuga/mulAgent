# Multica IM-first Chat Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn Multica Web into an IM-first multi-agent collaboration platform where the user creates direct chats and group chats, sends messages to agents, and receives inline agent work products.

**Architecture:** Web uses a chat-first workspace route with a 320px conversation list and a main chat area. Desktop keeps the existing floating chat overlay until a separate Desktop-first redesign. Direct and group chats share one backend model: legacy `chat_session` remains compatible, while `chat_session_agents` becomes the participant source of truth for IM/group behavior, and `chat_session_user_state` owns per-user pin/archive/read state.

**Tech Stack:** Next.js App Router, React, TanStack Query, Go + Chi + sqlc, PostgreSQL, gorilla/websocket, shadcn/Base UI components.

---

## Fixed Product Decisions

- Web primary route is `/{workspaceSlug}/chat`.
- Web hides the old Issues/Projects/Agents navigation behind a drawer in the chat shell.
- Web disables the floating `ChatWindow`/`ChatFab`; Desktop keeps them for now.
- Desktop is not converted to chat-first in this plan.
- Conversation list is fixed at `320px` on desktop.
- Mobile v1 uses IM navigation: list first, then tap a session to enter chat, with a back button.
- The product is personal multi-agent IM. v1 has no human group members.
- Direct chat is also represented as one participant in `chat_session_agents`.
- Group chat creation is a two-step flow: select multiple agents, then choose one selected agent as Orchestrator.
- A group chat without an Orchestrator cannot send messages.
- Single `@Agent` in a group routes directly to that agent.
- Zero `@Agent` or multiple `@Agent` routes to Orchestrator.
- Orchestrator dispatch must use structured plan/step APIs or CLI commands, not natural-language parsing.
- v1 execution is serial by default. Parallel execution is reserved by schema/API shape but not enabled by default.
- Each Orchestrator-generated step appears as a chat-stream system card and waits for user confirmation.
- First artifact version is a basic artifact card; inline code diff is deferred.
- Pin/archive/read state is per user via `chat_session_user_state`.
- Legacy `chat_session.status='archived'` remains as a compatibility bridge.

---

## Current Status After PR1-PR3 (Fixes Applied)

All critical integration gaps identified during local inspection have been fixed:

1. ✅ PR1: Floating chat is disabled on `/chat` route only. Other Web pages retain the floating chat (correct behavior per plan).
2. ✅ PR2: Schemas live in `packages/core/api/schemas.ts` (repo convention).
3. ✅ PR2: `view=im` handler batch-loads participants from `chat_session_agents`.
4. ✅ **FIXED:** `chat_session_agents` migration (`099_chat_session_agents.up.sql`) now exists with backfill for existing direct sessions.
5. ✅ PR3: `chat_session_user_state` migration (`098_chat_user_state.up.sql`) exists with legacy archived backfill.
6. ✅ **FIXED:** `view=im` handler now uses `ListChatSessionsForIMV2` with pin sort and archive filter.
7. ✅ **FIXED:** `ChatSession` TS type and `ChatSessionSchema` now include `is_pinned` and `archived_at`.
8. ✅ **FIXED:** All chat mutations (create, update, delete, markRead, pin, unpin, archive, unarchive) invalidate `chatKeys.imSessions(wsId)`.
9. ✅ **FIXED:** `UpdateChatSession` now accepts optional `status` field for legacy archive/unarchive compatibility with transition sync to `user_state`.

**Ready for PR2/PR3 validation and PR4 execution.**

### PR2/PR3 Final Verification Record

- ✅ `pnpm typecheck` — 6 packages pass
- ✅ `pnpm test` — 80 test files, 701 tests pass
- ✅ `go test ./internal/handler/ -v -count=1` — chat handler tests pass
- ⚠️ `go test ./...` — partial pass on Windows host. Failures in daemon/repocache/agent/redact due to Windows-specific issues (symlink permissions, path length, fake executables). Chat-related packages unaffected.
- ✅ Browser API verification — all 18 PR2/PR3 test cases pass (see verification table above)

**Implementation note:** v1 is a personal single-user product. The new archive/unarchive APIs (`POST /archive`, `POST /unarchive`) currently sync both `chat_session_user_state.archived_at` AND `chat_session.status` for consistency. When multi-user support is added in the future, new archive APIs should only write `user_state`, and `chat_session.status` should be reserved for legacy Desktop compatibility only.

---

## Browser Console Fetch Verification Standard

Whenever a PR adds or changes an API, the plan must include browser Console `fetch` verification snippets. Run these snippets from the logged-in Web app page, such as `http://localhost:3000/lpc/chat`, so the browser automatically sends the current `multica_auth` cookie. This avoids external-client CSRF failures caused by missing or mismatched cookies.

| Variable | Example | Notes |
|----------|---------|-------|
| `workspaceSlug` | `lpc` | Sent as `X-Workspace-Slug` |
| `sessionId` | UUID | Chat session under test |
| `agentId` | UUID | Existing workspace agent |
| `agentId2` | UUID | Second workspace agent for group tests |
| `q` | search text | Search keyword |

Paste this helper once per browser Console session:

```js
const workspaceSlug = "lpc";
const csrfToken =
  document.cookie.match(/(?:^|; )multica_csrf=([^;]+)/)?.[1] ?? "";

async function apiFetch(path, options = {}) {
  const headers = {
    "X-Workspace-Slug": workspaceSlug,
    "X-CSRF-Token": csrfToken,
    ...(options.body ? { "Content-Type": "application/json" } : {}),
    ...(options.headers ?? {}),
  };

  const response = await fetch(path, {
    credentials: "include",
    ...options,
    headers,
  });

  const text = await response.text();
  let body = text;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    // Keep plain text body.
  }

  console.log(options.method ?? "GET", path, response.status, body);
  return { status: response.status, body };
}
```

For state-changing requests, the helper sends `X-CSRF-Token` from the current `multica_csrf` cookie. If a request returns `403 {"error":"CSRF validation failed"}`, refresh the Web app, confirm you are still logged in, paste the helper again, and retry.

Every API verification should record:
- HTTP method and URL.
- Console command used.
- Request body, if any.
- Expected status.
- Required response fields.
- Database side effect, if the API writes data.
- UI side effect, if the endpoint feeds the Web IM shell.

---

## PR Order

| PR | Title | Key Deliverables |
|----|-------|------------------|
| 1 | Web Chat Route & IM Shell | `/chat` route, two-panel shell, drawer navigation, Web floating chat disabled |
| 2 | API Schema & Session List | Chat zod schemas with `parseWithFallback`, `view=im` endpoint, React Query session list |
| 3 | Pin/Archive/Read State | `chat_session_user_state`, backfill, legacy archive transition sync, pin/archive APIs |
| 4 | Direct Participant Model | `chat_session_agents` table, direct-chat backfill, participant reads, dual-write on create |
| 4.5 | Direct Chat Thread Rendering | Replace right-panel placeholder, render selected direct-chat messages, send input, pending/cancel, mark read |
| 5 | Group Chat & Message Model | Group creation wizard, Orchestrator validation, message fields, mention routing |
| 6 | Plan CLI & Step State | `chat_execution_plan`, `chat_execution_step`, structured Orchestrator plan APIs/CLI |
| 7 | Step Confirmation & Serial Lock | Step confirmation cards, edit/continue/skip, one running step per chat |
| 8 | Sandbox & Handoff | Per-participant `session_id`, handoff bundle, revision tracking |
| 9 | Artifact Cards | Basic artifact summary generation and chat-stream artifact cards |

---

## PR 1: Web Chat Route & IM Shell

**Status:** Completed candidate. Validate before relying on it.

**Files:**
- Create/modify: `apps/web/app/[workspaceSlug]/chat/page.tsx`
- Create/modify: `packages/views/chat/components/chat-shell.tsx`
- Create/modify: `packages/views/chat/components/chat-session-list.tsx`
- Create/modify: `packages/views/chat/components/chat-main-area.tsx`
- Create/modify: `packages/views/chat/components/chat-navigation-drawer.tsx`
- Modify: `packages/core/paths/paths.ts`
- Modify: `apps/web/app/[workspaceSlug]/(dashboard)/layout.tsx`

**Acceptance:**
- [ ] `/{workspaceSlug}/chat` renders as the main IM shell.
- [ ] Left panel is 320px on desktop and shows conversation-list UI.
- [ ] Empty state shows `No conversations yet`.
- [ ] Main area empty state shows `Select a conversation`.
- [ ] Drawer menu opens from the chat shell and can navigate to old pages.
- [ ] Web old pages such as `/{workspaceSlug}/issues` do not mount floating `ChatWindow`/`ChatFab`.
- [ ] Desktop still imports and renders `ChatWindow`/`ChatFab` in its existing flow.

**Verification commands:**

```bash
pnpm typecheck
pnpm --filter @multica/views test
```

**Manual smoke test:**

```bash
pnpm dev:web
```

Check:
- `http://localhost:3000/lpc/chat`
- `http://localhost:3000/lpc/issues`

---

## PR 2: API Schema & Session List

**Status:** Completed candidate. Validate carefully because it touches backend, API client compatibility, and the new IM session list.

**Files:**
- Modify: `packages/core/api/schemas.ts`
- Modify: `packages/core/api/client.ts`
- Modify: `packages/core/types/chat.ts`
- Modify: `packages/core/chat/queries.ts`
- Modify: `packages/views/chat/components/chat-session-list.tsx`
- Modify: `server/internal/handler/chat.go`
- Modify: `server/pkg/db/queries/chat.sql`
- Regenerate: `server/pkg/db/generated/chat.sql.go`

### Required PR2 Behavior

- [ ] UI-consumed chat API responses are parsed through `parseWithFallback`.
- [ ] Chat schemas live in `packages/core/api/schemas.ts`, matching current repo convention.
- [ ] `api.listChatSessions({ view: "im" })` sends `GET /api/chat/sessions?view=im`.
- [ ] `chatIMSessionsOptions(wsId)` uses a workspace-scoped React Query key.
- [ ] `ChatSessionList` reads from `chatIMSessionsOptions(wsId)`.
- [ ] Legacy `GET /api/chat/sessions` remains compatible with Desktop floating chat.
- [ ] `GET /api/chat/sessions?view=im` returns sessions sorted by recent activity.
- [ ] `GET /api/chat/sessions?view=im&q=...` searches title, participant name, and latest message preview.
- [ ] Empty database state still renders `No conversations yet`.

### PR2 Blocker Check: Participant Table Dependency

Because current SQL references `chat_session_agents`, PR2 is only deployable if one of these is true:

- [ ] `chat_session_agents` migration exists before any PR2 deployment; or
- [ ] PR2 handler does not call `ListChatSessionParticipantsBySessionIDs` until PR4 lands.

The preferred path for this project is to move the `chat_session_agents` migration and direct-chat backfill into PR4, then require PR4 to be applied before enabling participant reads in production. If PR2 is already merged and the handler already calls `ListChatSessionParticipantsBySessionIDs`, add a small corrective PR before PR4:

- Create `chat_session_agents`.
- Backfill every existing direct session as one participant.
- Keep group-only columns nullable or absent until PR5.
- Re-run sqlc and Go tests.

### PR2 Engineering Boundaries

- [ ] Do not make `view=im` the default for `GET /api/chat/sessions`; old Desktop clients must keep getting the legacy-compatible list unless they explicitly pass `view=im`.
- [ ] Do not remove `chat_session.agent_id`; direct chats and older Desktop builds still depend on it.
- [ ] Do not store fetched sessions in Zustand. React Query remains the only owner of session server state.
- [ ] Search can be implemented in Go after the DB query for v1, but the API contract must remain `q=<keyword>` so it can move to SQL later without changing the frontend.
- [ ] `q` search must be case-insensitive.
- [ ] Empty or missing optional IM fields must not crash Web or Desktop.
- [ ] Zod schemas must be lenient: string enums use `z.string()`, arrays default to `[]`, unknown fields pass through with `.loose()`.
- [ ] `participants` is optional until `chat_session_agents` is guaranteed to exist in deployed migrations.
- [ ] `last_message_preview` must be short enough for list UI. If truncation is server-side, document the max length; if not, truncate in UI.
- [ ] `view=im` must enforce the same workspace, creator, and private-agent visibility checks as the legacy list.
- [ ] `CreateChatSession`, `UpdateChatSession`, `DeleteChatSession`, and `MarkChatSessionRead` must invalidate `chatKeys.imSessions(wsId)` once the IM list is active.

### PR2 Verification Indicators

Run these static checks:

```bash
rg -n "ChatSessionSchema|ChatMessageSchema|EMPTY_CHAT_SESSION|parseWithFallback" packages/core/api/schemas.ts packages/core/api/client.ts
rg -n "view: \"im\"|imSessions|chatIMSessionsOptions" packages/core/chat packages/views/chat
rg -n "ListChatSessionsForIM|ListChatSessionParticipantsBySessionIDs" server/pkg/db/queries/chat.sql server/internal/handler/chat.go
rg -n "CREATE TABLE chat_session_agents|chat_session_agents" server/migrations server/pkg/db/queries/chat.sql
```

Expected:
- `parseWithFallback` is used by chat methods in `packages/core/api/client.ts`.
- `chatIMSessionsOptions` calls `api.listChatSessions({ view: "im" })`.
- `ChatSessionList` uses React Query data, not local duplicated server state.
- If `ListChatSessionParticipantsBySessionIDs` is called, a `chat_session_agents` migration must exist.

Run backend generation/test checks:

```bash
make sqlc
make test
```

Expected:
- sqlc generation succeeds with no query/table mismatch.
- Go tests pass.

Run frontend checks:

```bash
pnpm typecheck
pnpm test
```

Expected:
- TypeScript sees `ChatSession` fields used by the list.
- Vitest tests pass.

Manual/API checks:
- [ ] `GET /api/chat/sessions` still returns legacy-compatible active sessions.
- [ ] `GET /api/chat/sessions?status=all` still returns active and archived legacy sessions.
- [ ] `GET /api/chat/sessions?view=im` returns an array.
- [ ] Each IM row can include `kind`, `participants`, `last_message_preview`, and `last_message_at`.
- [ ] Search by title works.
- [ ] Search by latest message content works.
- [ ] Search by participant name works once participants are available.

### PR2 Browser Console Fetch Requests

#### 1. Legacy session list

```js
await apiFetch("/api/chat/sessions");
```

Expected:
- Status `200`.
- Response is an array.
- Existing direct-chat fields remain present: `id`, `workspace_id`, `agent_id`, `creator_id`, `title`, `status`, `has_unread`, `created_at`, `updated_at`.
- Desktop-compatible behavior is unchanged.

#### 2. Legacy all session list

```js
await apiFetch("/api/chat/sessions?status=all");
```

Expected:
- Status `200`.
- Response includes active and legacy archived sessions owned by the user.
- This endpoint is not the IM archive list; it exists for compatibility.

#### 3. IM session list

```js
await apiFetch("/api/chat/sessions?view=im");
```

Expected:
- Status `200`.
- Response is an array.
- Each item keeps legacy fields and may include `kind`, `participants`, `last_message_preview`, `last_message_at`.
- Sessions are sorted by latest message time, falling back to `chat_session.updated_at`.

#### 4. IM search by title/message/participant

```js
const q = "keyword";
await apiFetch(`/api/chat/sessions?view=im&q=${encodeURIComponent(q)}`);
```

Expected:
- Status `200`.
- Search matches session title.
- Search matches latest message preview.
- Search matches participant name after `chat_session_agents` exists.
- Non-matching sessions are absent.

#### 5. Create direct chat smoke request

```js
const agentId = "replace-with-agent-id";
await apiFetch("/api/chat/sessions", {
  method: "POST",
  body: JSON.stringify({
    agent_id: agentId,
    title: "Console direct chat",
  }),
});
```

Expected:
- Status `201`.
- Response includes `id`, `agent_id`, `title`, and `status`.
- After creation, `GET /api/chat/sessions?view=im` includes the new session.

---

## PR 3: Pin/Archive/Read State

**Status:** Completed candidate, but local inspection shows likely integration gaps. Validate and fix before PR4.

**Files:**
- Create: `server/migrations/098_chat_user_state.up.sql`
- Create: `server/migrations/098_chat_user_state.down.sql`
- Modify: `server/pkg/db/queries/chat.sql`
- Regenerate: `server/pkg/db/generated/chat.sql.go`
- Modify: `server/internal/handler/chat.go`
- Modify: `server/cmd/server/router.go`
- Modify: `packages/core/api/client.ts`
- Modify: `packages/core/types/chat.ts`
- Modify: `packages/core/api/schemas.ts`
- Modify: `packages/core/chat/mutations.ts`
- Modify: `packages/core/chat/queries.ts`
- Modify: `packages/views/chat/components/chat-session-list.tsx`

### Required PR3 Behavior

- [ ] `chat_session_user_state` stores `pinned_at`, `archived_at`, and `last_read_at` per `chat_session_id + user_id`.
- [ ] Migration backfills legacy archived sessions:

```sql
INSERT INTO chat_session_user_state (chat_session_id, user_id, workspace_id, archived_at)
SELECT cs.id, cs.creator_id, cs.workspace_id, cs.updated_at
FROM chat_session cs
WHERE cs.status = 'archived'
ON CONFLICT (chat_session_id, user_id) DO NOTHING;
```

- [ ] New archive API writes `chat_session_user_state.archived_at`.
- [ ] New unarchive API clears `chat_session_user_state.archived_at`.
- [ ] New pin API writes `chat_session_user_state.pinned_at`.
- [ ] New unpin API clears `chat_session_user_state.pinned_at`.
- [ ] Legacy `PATCH /api/chat/sessions/{id}` with `{ "status": "archived" }` still writes legacy `chat_session.status` and mirrors to `user_state.archived_at`.
- [ ] Legacy unarchive with `{ "status": "active" }` clears `user_state.archived_at`.
- [ ] The router/handler path for legacy status update is real: either `UpdateChatSession` accepts `status`, or `PATCH /api/chat/sessions/{id}` dispatches to `UpdateChatSessionStatus` when a `status` field is present.
- [ ] `GET /api/chat/sessions?view=im` uses `ListChatSessionsForIMV2`, not `ListChatSessionsForIM`.
- [ ] Main IM list hides user-archived sessions.
- [ ] Archive-list API or query option can include archived sessions.
- [ ] Pinned sessions sort above unpinned sessions.
- [ ] `ChatSession` TypeScript type includes `is_pinned?: boolean` and `archived_at?: string | null`.
- [ ] `ChatSessionSchema` accepts and preserves `is_pinned` and `archived_at`.
- [ ] Mutations invalidate both `chatKeys.sessions(wsId)` and `chatKeys.imSessions(wsId)`.
- [ ] Create, update, delete, and mark-read flows also invalidate or update the IM cache.

### PR3 Integration Fixes If Current Code Matches Local Inspection

- [ ] Replace the `view == "im"` handler branch from `ListChatSessionsForIM` to `ListChatSessionsForIMV2`.
- [ ] Pass `includeArchived` from a clear query parameter such as `archived=true`.
- [ ] Add `IsPinned bool` and `ArchivedAt *string` to `ChatSessionResponse`.
- [ ] Map `row.PinnedAt.Valid` to `is_pinned`.
- [ ] Map `row.ArchivedAt.Valid` to `archived_at`.
- [ ] Keep legacy non-IM list behavior unchanged for Desktop compatibility.
- [ ] Add `archived_at` to `ChatSessionSchema` and `ChatSession` type.
- [ ] Add IM cache invalidation to create/update/delete/read mutations.
- [ ] Merge `UpdateChatSessionStatus` behavior into `UpdateChatSession`, or register a non-conflicting compatibility route. Since `PATCH /api/chat/sessions/{id}` is already used for title updates, the safer v1 fix is one handler that accepts optional `title` and optional `status`, validates at least one field, and applies the correct branch.

### PR3 Engineering Boundaries

- [ ] `chat_session_user_state` is user-specific. Pin/archive/read changes must always include the authenticated `user_id`; never update all users for one session.
- [ ] New archive/unarchive APIs must not change `chat_session.status` unless explicitly executing the legacy compatibility path.
- [ ] Legacy `PATCH status` path may continue to write `chat_session.status`, but it must mirror into `chat_session_user_state` for the current user.
- [ ] Legacy title update and legacy status update must coexist on `PATCH /api/chat/sessions/{id}` without breaking either request shape.
- [ ] `UpsertChatSessionUserState` must not accidentally clear existing fields when writing only one state. Pinning must not clear `archived_at`; archiving must not clear `pinned_at`; marking read must not clear either.
- [ ] `ClearChatSessionUserArchived` and `ClearChatSessionUserPinned` must be idempotent.
- [ ] Main `view=im` list hides rows with `archived_at IS NOT NULL` for the current user.
- [ ] Archived list uses `view=im&archived=true` for v1; do not create a second endpoint unless the UI needs a different shape.
- [ ] Pinned sort order applies only in IM lists, not legacy Desktop lists.
- [ ] `is_pinned` is derived from `pinned_at.Valid`; do not persist a redundant boolean column.
- [ ] `archived_at` in API response is nullable and belongs to user_state, not legacy `chat_session.status`.
- [ ] Backfill must use `creator_id` as the user for legacy direct sessions because v1 has no multi-user chat membership.
- [ ] If backfill is re-run in dev, it must be idempotent via `ON CONFLICT`.

### PR3 Verification Indicators

Run static checks:

```bash
rg -n "CREATE TABLE chat_session_user_state|Backfill archived" server/migrations
rg -n "ListChatSessionsForIMV2|UpsertChatSessionUserState|ClearChatSessionUserArchived|ClearChatSessionUserPinned" server/pkg/db/queries/chat.sql server/internal/handler/chat.go
rg -n "Post\\(\"/pin\"|Delete\\(\"/pin\"|Post\\(\"/archive\"|Post\\(\"/unarchive\"" server/cmd/server/router.go
rg -n "is_pinned|archived_at" packages/core/types/chat.ts packages/core/api/schemas.ts server/internal/handler/chat.go
rg -n "imSessions" packages/core/chat/mutations.ts packages/core/chat/queries.ts
```

Expected:
- Migration exists and backfills archived sessions.
- Router registers pin, unpin, archive, and unarchive routes.
- Handler uses `ListChatSessionsForIMV2` for `view=im`.
- Backend response, TS type, and zod schema all include `is_pinned` and `archived_at`.
- All mutations that can change the visible session list invalidate `chatKeys.imSessions(wsId)`.

Run backend checks:

```bash
make sqlc
make test
```

Expected:
- Generated sqlc code matches current SQL.
- Existing Go tests pass.
- Add or verify handler tests for pin/archive transition behavior.

Run frontend checks:

```bash
pnpm typecheck
pnpm test
```

Expected:
- `ChatSession` fields used by UI are typed.
- API schema fallback tests pass.

Manual/API checks:
- [ ] Create two chat sessions with different latest message times; IM list sorts by latest activity.
- [ ] Pin the older session; it appears above the newer unpinned session.
- [ ] Unpin it; activity sort is restored.
- [ ] Archive a session via `POST /api/chat/sessions/{id}/archive`; it disappears from the main IM list.
- [ ] Unarchive it via `POST /api/chat/sessions/{id}/unarchive`; it appears again.
- [ ] Archive a session via legacy `PATCH /api/chat/sessions/{id}` with `{ "status": "archived" }`; `chat_session_user_state.archived_at` is written.
- [ ] Backfilled legacy archived sessions are absent from the main IM list after migration.
- [ ] Archived sessions can still be reached through the planned archived-list entry.

### Additional PR2/PR3 Validation Before PR4

Based on current PR2/PR3 acceptance results, these six checks should be completed before starting PR4:

- [ ] Legacy list compatibility: `GET /api/chat/sessions` and `GET /api/chat/sessions?status=all` still return legacy-compatible rows.
- [ ] IM search: `view=im&q=...` works for session title, latest message content, and Agent name.
- [ ] Unpin: `DELETE /api/chat/sessions/{id}/pin` clears pin state and restores activity ordering.
- [ ] Mark read: `POST /api/chat/sessions/{id}/read` clears `has_unread` and IM list reflects it.
- [ ] IM field shape: `view=im` rows expose the fields used by the Web list: `id`, `agent_id`, `status`, `has_unread`, `last_message_preview`, `last_message_at`, `is_pinned`, `archived_at`, and `participants` when available.
- [ ] Local engineering checks pass: `make sqlc`, `pnpm typecheck`, `make test`, and preferably `pnpm test`.

Browser Console snippets for the first five checks:

```js
await apiFetch("/api/chat/sessions");
await apiFetch("/api/chat/sessions?status=all");
```

```js
const q = "replace-with-title-message-or-agent-name";
await apiFetch(`/api/chat/sessions?view=im&q=${encodeURIComponent(q)}`);
```

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}/pin`, { method: "DELETE" });
await apiFetch("/api/chat/sessions?view=im");
```

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}/read`, { method: "POST" });
await apiFetch("/api/chat/sessions?view=im");
```

```js
const { body: sessions } = await apiFetch("/api/chat/sessions?view=im");
console.table(
  (sessions ?? []).map((s) => ({
    id: s.id,
    agent_id: s.agent_id,
    status: s.status,
    has_unread: s.has_unread,
    last_message_preview: s.last_message_preview,
    last_message_at: s.last_message_at,
    is_pinned: s.is_pinned,
    archived_at: s.archived_at,
    participants: Array.isArray(s.participants) ? s.participants.length : undefined,
  })),
);
```

### PR3 Browser Console Fetch Requests

#### 1. Pin session

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}/pin`, { method: "POST" });
```

Expected:
- Status `204`.
- `GET /api/chat/sessions?view=im` returns this session before unpinned sessions.
- Response item has `is_pinned: true` once backend response mapping is complete.

#### 2. Unpin session

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}/pin`, { method: "DELETE" });
```

Expected:
- Status `204`.
- `GET /api/chat/sessions?view=im` no longer prioritizes the session by pin state.
- Response item has `is_pinned: false` or no pinned signal after unpin.

#### 3. Archive session with new API

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}/archive`, { method: "POST" });
```

Expected:
- Status `204`.
- A row exists in `chat_session_user_state` for `sessionId + current user`.
- `archived_at` is non-null.
- `GET /api/chat/sessions?view=im` does not include this session.

#### 4. Include archived sessions in IM list

```js
await apiFetch("/api/chat/sessions?view=im&archived=true");
```

Expected:
- Status `200`.
- Archived sessions for the current user are included.
- Archived rows include `archived_at` once backend response mapping is complete.
- This request is the basis for the future “Archived conversations” drawer/list entry.

#### 5. Unarchive session with new API

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}/unarchive`, { method: "POST" });
```

Expected:
- Status `204`.
- `chat_session_user_state.archived_at` is null.
- `GET /api/chat/sessions?view=im` includes the session again.

#### 6. Legacy archive compatibility

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}`, {
  method: "PATCH",
  body: JSON.stringify({ status: "archived" }),
});
```

Expected:
- Status `200` after PR3 compatibility is correctly wired.
- `chat_session.status` becomes `archived`.
- `chat_session_user_state.archived_at` is written for current user.
- If this returns `400 title is required`, PR3 compatibility is not wired yet.

#### 7. Legacy unarchive compatibility

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}`, {
  method: "PATCH",
  body: JSON.stringify({ status: "active" }),
});
```

Expected:
- Status `200` after PR3 compatibility is correctly wired.
- `chat_session.status` becomes `active`.
- `chat_session_user_state.archived_at` is cleared for current user.

#### 8. Mark session read

```js
const sessionId = "replace-with-session-id";
await apiFetch(`/api/chat/sessions/${sessionId}/read`, { method: "POST" });
```

Expected:
- Status `204`.
- Legacy unread signal clears.
- IM list cache invalidation or realtime invalidation causes `has_unread` to become false in Web.

---

## PR 4: Direct Participant Model

**Goal:** Make participants a first-class backend concept while preserving direct-chat compatibility.

**Files:**
- Create: `server/migrations/099_chat_session_agents.up.sql`
- Create: `server/migrations/099_chat_session_agents.down.sql`
- Modify: `server/pkg/db/queries/chat.sql`
- Regenerate: `server/pkg/db/generated/chat.sql.go`
- Modify: `server/internal/handler/chat.go`

### Task 1: Add `chat_session_agents`

**PK decision:** v1 uses composite PK `(chat_session_id, agent_id)` instead of `id UUID PRIMARY KEY`. Rationale:
- v1 is personal single-user IM; no member lifecycle (remove/rejoin) needed.
- Composite PK naturally prevents duplicate active participants.
- Avoids table rebuild that would risk already-verified PR2/PR3 data.
- Future multi-user extension can add `id UUID` + partial unique index via new migration.

- [ ] Create table:

```sql
CREATE TABLE chat_session_agents (
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
  role TEXT NOT NULL DEFAULT 'participant',
  session_id TEXT,
  runtime_id UUID REFERENCES agent_runtime(id) ON DELETE SET NULL,
  work_dir TEXT,
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  removed_at TIMESTAMPTZ,
  PRIMARY KEY (chat_session_id, agent_id),
  CHECK (role IN ('participant', 'orchestrator'))
);

CREATE INDEX idx_chat_session_agents_agent
  ON chat_session_agents(agent_id, chat_session_id)
  WHERE removed_at IS NULL;

CREATE UNIQUE INDEX idx_chat_session_agents_active_unique
  ON chat_session_agents(chat_session_id, agent_id)
  WHERE removed_at IS NULL;

CREATE UNIQUE INDEX idx_chat_session_agents_one_orchestrator
  ON chat_session_agents(chat_session_id)
  WHERE role = 'orchestrator' AND removed_at IS NULL;
```

- [ ] Backfill every existing direct chat:

```sql
INSERT INTO chat_session_agents (
  chat_session_id,
  agent_id,
  role,
  session_id,
  runtime_id,
  work_dir,
  joined_at
)
SELECT
  id,
  agent_id,
  'participant',
  session_id,
  runtime_id,
  work_dir,
  created_at
FROM chat_session
WHERE NOT EXISTS (
  SELECT 1
  FROM chat_session_agents csa
  WHERE csa.chat_session_id = chat_session.id
    AND csa.agent_id = chat_session.agent_id
    AND csa.removed_at IS NULL
);
```

- [ ] Run:

```bash
make migrate-up
make sqlc
make test
```

### Task 2: Dual-write direct chat creation

- [ ] In `CreateChatSession`, after creating `chat_session`, insert one `chat_session_agents` row.
- [ ] If participant insert fails, roll back the session creation in the same transaction.
- [ ] Keep `chat_session.agent_id` as the direct agent for legacy compatibility.

### Task 3: Read participants from the join table

- [ ] `view=im` returns direct sessions with one participant.
- [ ] Direct session `kind` is `direct`.
- [ ] Participant role for direct sessions is `participant`.
- [ ] Private-agent access checks still apply to direct participants.

### PR4 Engineering Boundaries

- [ ] PR4 is a foundation PR and must be deployed before any handler path depends on `chat_session_agents`.
- [ ] PR4 keeps `(chat_session_id, agent_id)` as the primary key for v1 because there is no member lifecycle UI yet. If future participant remove/re-add history is required, add a row-level `id UUID` in a later dedicated migration.
- [ ] Only one active participant row per `chat_session_id + agent_id` is allowed.
- [ ] Only one active Orchestrator per chat is allowed by partial unique index, even though direct chats have no Orchestrator yet.
- [ ] Direct-chat backfill must be idempotent.
- [ ] Direct-chat backfill must copy existing `session_id`, `runtime_id`, and `work_dir` so old agent memory resumes continue to work after migration.
- [ ] Direct-chat creation must be transactional: create `chat_session`, create `chat_session_agents`, commit once.
- [ ] If participant dual-write fails, the `chat_session` row must not be left orphaned.
- [ ] Do not introduce group creation in PR4. It only prepares the participant model.
- [ ] Do not remove or stop updating `chat_session.agent_id`, `session_id`, `runtime_id`, or `work_dir` yet.
- [ ] Private-agent access checks still use the session's effective direct agent in PR4. Group-wide access comes in PR5.
- [ ] `ListChatSessionParticipantsBySessionIDs` must tolerate an empty session ID slice and return no rows.
- [ ] If `participants` is unexpectedly empty for a legacy direct session, the API should fall back to `chat_session.agent_id` rather than returning an unusable list row.

### PR4 Browser Console Fetch Requests

#### 1. Create direct chat after participant dual-write

```js
const agentId = "replace-with-agent-id";
const { body: createdSession } = await apiFetch("/api/chat/sessions", {
  method: "POST",
  body: JSON.stringify({
    agent_id: agentId,
    title: "Console PR4 direct participant",
  }),
});
console.log("Created session:", createdSession?.id);
```

Expected:
- Status `201`.
- Response includes the new `sessionId`.
- Database has one active `chat_session_agents` row for that `sessionId` and `agentId`.
- `chat_session.agent_id` still equals `agentId`.

#### 2. IM list includes direct participant

```js
await apiFetch("/api/chat/sessions?view=im");
```

Expected:
- Status `200`.
- The new session has `kind: "direct"`.
- `participants` has one item.
- The participant `agent_id` equals `{{agentId}}`.

---

## PR 4.5: Direct Chat Thread Rendering

**Goal:** Make the Web chat-first page actually usable for existing direct chats before adding group chat.

**Why this PR exists:** PR4 made `chat_session_agents` reliable, but the right panel still shows `Thread rendering coming in PR 4`. PR4.5 is a narrow UI bridge: selected direct conversations should display history, accept a message, show the running task state, and mark unread conversations as read. It must not introduce group orchestration yet.

**Files:**
- Modify: `packages/views/chat/components/chat-main-area.tsx`
- Modify: `packages/views/chat/components/chat-input.tsx`
- Create: `packages/views/chat/components/direct-chat-thread.tsx`
- Test: `packages/views/chat/components/direct-chat-thread.test.tsx`
- Optional helper if duplication becomes noisy: `packages/views/chat/lib/direct-chat-send.ts`

### PR4.5 Engineering Boundaries

- [ ] Only direct-chat thread rendering is in scope. Group creation, group headers, mention routing, and Orchestrator dispatch remain PR5.
- [ ] Do not re-enable Web `ChatWindow` or `ChatFab`. Desktop keeps using the existing floating chat window.
- [ ] Reuse `ChatMessageList`, `ChatMessageSkeleton`, `ChatInput`, and `TaskStatusPill` behavior instead of creating a second message renderer.
- [ ] The selected session already exists. PR4.5 must not lazy-create sessions from the right panel.
- [ ] Message send uses existing `api.sendChatMessage(sessionId, content, attachmentIds)`.
- [ ] Use React Query cache keys from `@multica/core/chat/queries`. Do not put fetched messages into Zustand.
- [ ] `ChatShell` may keep `activeSessionId` as local state for this PR. A URL-driven selected-session state can be a later UX improvement.
- [ ] `ChatInput` currently derives draft storage from the global chat Zustand store. PR4.5 must add optional draft/editor key overrides or explicitly sync the selected Web session into the store. Prefer key overrides because it keeps Web main chat independent from Desktop floating chat behavior.
- [ ] If the selected session is archived, render messages read-only and disable `ChatInput`.
- [ ] If the selected session is not `direct`, render a small unsupported-state message until PR5 lands.
- [ ] Mark read on open only when the selected IM session has `has_unread === true`.
- [ ] Send/cancel must invalidate `chatKeys.messages(sessionId)`, `chatKeys.pendingTask(sessionId)`, and `chatKeys.imSessions(wsId)` where appropriate.
- [ ] Keep attachment upload disabled in PR4.5 unless you deliberately wire `useFileUpload` with `chatSessionId`. Plain text send is enough for acceptance.
- [ ] Do not change backend schema or API in PR4.5.

### Task 1: Add optional draft keys to `ChatInput`

- [ ] Modify `packages/views/chat/components/chat-input.tsx`.

Add two optional props:

```tsx
interface ChatInputProps {
  onSend: (content: string, attachmentIds?: string[]) => void;
  onUploadFile?: (file: File) => Promise<UploadResult | null>;
  onStop?: () => void;
  isRunning?: boolean;
  disabled?: boolean;
  noAgent?: boolean;
  agentName?: string;
  leftAdornment?: ReactNode;
  rightAdornment?: ReactNode;
  topSlot?: ReactNode;
  draftKeyOverride?: string;
  editorKeyOverride?: string;
}
```

Then keep the current ChatWindow behavior as the default path:

```tsx
const fallbackDraftKey =
  activeSessionId ?? `${DRAFT_NEW_SESSION}:${selectedAgentId ?? ""}`;
const draftKey = draftKeyOverride ?? fallbackDraftKey;
const editorKey = editorKeyOverride ?? selectedAgentId ?? "no-agent";
```

Expected:
- Existing `ChatWindow` does not need to pass either prop.
- Web main chat can pass `draftKeyOverride={sessionId}` so drafts are scoped to the selected IM conversation, not the old floating-window store state.

### Task 2: Add a direct-thread component

- [ ] Create `packages/views/chat/components/direct-chat-thread.tsx`.

Use this shape. Keep the component small; it should compose existing chat primitives and own only selected-session behavior.

```tsx
"use client";

import { useCallback, useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  chatIMSessionsOptions,
  chatKeys,
  chatMessagesOptions,
  pendingChatTaskOptions,
} from "@multica/core/chat/queries";
import { useMarkChatSessionRead } from "@multica/core/chat/mutations";
import { useAgentPresenceDetail } from "@multica/core/agents";
import type { ChatMessage, ChatPendingTask } from "@multica/core/types";
import { ChatInput } from "./chat-input";
import { ChatMessageList, ChatMessageSkeleton } from "./chat-message-list";

interface DirectChatThreadProps {
  sessionId: string;
}

export function DirectChatThread({ sessionId }: DirectChatThreadProps) {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const markRead = useMarkChatSessionRead();

  const { data: sessions = [] } = useQuery(chatIMSessionsOptions(wsId));
  const session = sessions.find((item) => item.id === sessionId);
  const directAgent = session?.participants?.[0];
  const agentId = directAgent?.agent_id ?? session?.agent_id;
  const isArchived = session?.status === "archived" || !!session?.archived_at;

  const { data: rawMessages, isLoading } = useQuery(chatMessagesOptions(sessionId));
  const messages = rawMessages ?? [];
  const { data: pendingTask } = useQuery(pendingChatTaskOptions(sessionId));
  const pendingTaskId = pendingTask?.task_id ?? null;

  const presence = useAgentPresenceDetail(wsId, agentId);
  const availability = presence === "loading" ? undefined : presence.availability;

  useEffect(() => {
    if (!session?.has_unread) return;
    markRead.mutate(sessionId);
  }, [markRead, session?.has_unread, sessionId]);

  const handleSend = useCallback(
    async (content: string, attachmentIds?: string[]) => {
      const sentAt = new Date().toISOString();
      const optimistic: ChatMessage = {
        id: `optimistic-${Date.now()}`,
        chat_session_id: sessionId,
        role: "user",
        content,
        task_id: null,
        created_at: sentAt,
      };

      qc.setQueryData<ChatMessage[]>(
        chatKeys.messages(sessionId),
        (old) => (old ? [...old, optimistic] : [optimistic]),
      );
      qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId), {
        task_id: `optimistic-${optimistic.id}`,
        status: "queued",
        created_at: sentAt,
      });

      const result = await api.sendChatMessage(sessionId, content, attachmentIds);
      qc.setQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId), {
        task_id: result.task_id,
        status: "queued",
        created_at: result.created_at,
      });
      qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
      qc.invalidateQueries({ queryKey: chatKeys.imSessions(wsId) });
    },
    [qc, sessionId, wsId],
  );

  const handleStop = useCallback(() => {
    if (!pendingTaskId) return;
    qc.setQueryData(chatKeys.pendingTask(sessionId), {});
    qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
    api.cancelTaskById(pendingTaskId).finally(() => {
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
    });
  }, [pendingTaskId, qc, sessionId]);

  if (isLoading) return <ChatMessageSkeleton />;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ChatMessageList
        messages={messages}
        pendingTask={pendingTask}
        availability={availability}
      />
      <ChatInput
        onSend={handleSend}
        onStop={handleStop}
        isRunning={!!pendingTaskId}
        disabled={isArchived}
        agentName={directAgent?.name}
        draftKeyOverride={sessionId}
        editorKeyOverride={agentId ?? sessionId}
      />
    </div>
  );
}
```

- [ ] If TypeScript reports `ChatPendingTask` cannot accept `{}` in `handleStop`, use `null` and update the call site consistently with `pendingChatTaskOptions`' return type.

### Task 3: Replace the placeholder in `ChatMainArea`

- [ ] Modify `packages/views/chat/components/chat-main-area.tsx`.

Expected behavior:
- No selected session keeps the existing `Select a conversation` empty state.
- Direct selected session renders `DirectChatThread`.
- Group selected session renders a temporary unsupported state until PR5.

Use this structure:

```tsx
"use client";

import { MessageSquarePlus, Users } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { chatIMSessionsOptions } from "@multica/core/chat/queries";
import { DirectChatThread } from "./direct-chat-thread";

interface ChatMainAreaProps {
  sessionId?: string;
}

export function ChatMainArea({ sessionId }: ChatMainAreaProps) {
  const wsId = useWorkspaceId();
  const { data: sessions = [] } = useQuery(chatIMSessionsOptions(wsId));
  const session = sessionId ? sessions.find((item) => item.id === sessionId) : null;

  if (!sessionId) {
    return (
      <div className="flex flex-1 items-center justify-center">
        <div className="flex flex-col items-center gap-3 text-muted-foreground">
          <div className="flex h-12 w-12 items-center justify-center rounded-full border border-border bg-muted/40">
            <MessageSquarePlus className="h-5 w-5" />
          </div>
          <p className="text-sm">Select a conversation</p>
        </div>
      </div>
    );
  }

  if (session?.kind === "group") {
    return (
      <div className="flex flex-1 items-center justify-center">
        <div className="flex flex-col items-center gap-3 text-muted-foreground">
          <div className="flex h-12 w-12 items-center justify-center rounded-full border border-border bg-muted/40">
            <Users className="h-5 w-5" />
          </div>
          <p className="text-sm">Group chat rendering comes in PR5</p>
        </div>
      </div>
    );
  }

  return <DirectChatThread sessionId={sessionId} />;
}
```

### Task 4: Add focused component tests

- [ ] Add `packages/views/chat/components/direct-chat-thread.test.tsx`.
- [ ] Mock `ContentEditor` through `ChatInput` if the editor makes the test heavy. The test only needs to prove PR4.5 wiring, not Tiptap behavior.

Test cases:
- `ChatInput` uses `draftKeyOverride` when provided and keeps the existing fallback behavior when it is not provided.
- selected direct session fetches and renders existing messages.
- sending text calls `api.sendChatMessage(sessionId, content)` and optimistically shows the user message.
- session with `has_unread: true` calls `useMarkChatSessionRead().mutate(sessionId)`.
- archived selected session disables the input.

Suggested test command:

```bash
pnpm --filter @multica/views test -- chat
```

Expected:
- New direct-thread tests pass.
- Existing `chat-input` and `context-anchor` tests still pass.

### Task 5: Manual browser validation

Run the app with the existing Docker compose environment, open `http://localhost:3000/lpc/chat`, then validate:

- [ ] Select an existing direct session. The right panel no longer shows `Thread rendering coming in PR 4`.
- [ ] Existing messages load in the right panel.
- [ ] Type an unsent draft in session A, switch to session B, then switch back to session A. The draft is still scoped to session A.
- [ ] Send a plain text message. A user bubble appears immediately.
- [ ] While the agent is running, the pending task pill appears and the input shows stop state.
- [ ] When the assistant reply completes, the message list refreshes and the left session preview/recent activity updates.
- [ ] Select an archived session from the archived list if available. Messages render read-only and the input is disabled.
- [ ] Refresh the page with a selected session active. It is acceptable in PR4.5 if selection resets because URL-selected sessions are not required yet.
- [ ] Other pages such as `/lpc/issues` still do not show Web floating chat.

### Task 6: Verification and commit

Run:

```bash
pnpm typecheck
pnpm test
cd server
go test ./internal/handler/ -v -count=1
```

Expected:
- TypeScript passes.
- Vitest suite passes.
- Go chat handler tests still pass; PR4.5 should not need backend changes.

Commit:

```bash
git add packages/views/chat/components/chat-input.tsx packages/views/chat/components/chat-main-area.tsx packages/views/chat/components/direct-chat-thread.tsx packages/views/chat/components/direct-chat-thread.test.tsx docs/superpowers/plans/2026-05-26-chat-redesign.md
git commit -m "feat(PR4.5): render direct chat thread in web shell"
```

---

## PR 5: Group Chat & Message Model

**Goal:** Add group chats with manually selected Orchestrator and structured mention routing.

**Files:**
- Modify: `server/migrations/100_group_chat_fields.up.sql`
- Modify: `server/migrations/100_group_chat_fields.down.sql`
- Modify: `server/pkg/db/queries/chat.sql`
- Modify: `server/internal/handler/chat.go`
- Modify: `packages/core/types/chat.ts`
- Modify: `packages/core/api/schemas.ts`
- Modify: `packages/views/chat/components/new-group-chat-dialog.tsx`
- Modify: `packages/views/chat/components/new-direct-chat-dialog.tsx`
- Modify: `packages/views/chat/components/chat-session-list.tsx`
- Modify: `packages/views/chat/components/chat-main-area.tsx`

### Required Behavior

- [ ] Add `chat_session.kind` with values `direct` and `group`.
- [ ] Add `chat_session.orchestrator_agent_id`.
- [ ] Add `chat_session.title_source` with values `manual`, `agent_names`, and `first_message`.
- [ ] Add `chat_message.agent_id`.
- [ ] Add `chat_message.message_type`.
- [ ] Add `chat_message.metadata JSONB`.
- [ ] Allow `chat_message.role='system'`.
- [ ] Create direct chat from one selected agent.
- [ ] Create group chat from multiple selected agents.
- [ ] Group creation requires Orchestrator chosen from selected agents.
- [ ] On group creation, insert one `orchestrator` participant and N `participant` rows.
- [ ] New group enters an empty session; first message is sent later from input or task-creation flow.
- [ ] Direct title defaults to agent name.
- [ ] Group title defaults to joined agent names.
- [ ] After first user message, title can be auto-updated if `title_source='agent_names'`.

### API Contract

Use one create endpoint for both direct and group chats:

```http
POST /api/chat/sessions
```

Direct request:

```json
{
  "kind": "direct",
  "agent_id": "agent uuid",
  "title": "optional title"
}
```

Group request:

```json
{
  "kind": "group",
  "agent_ids": ["agent uuid", "agent uuid"],
  "orchestrator_agent_id": "agent uuid",
  "title": "optional title"
}
```

Send-message request:

```json
{
  "content": "message text",
  "attachment_ids": ["attachment uuid"],
  "mention_ids": ["agent uuid"]
}
```

### PR5 Engineering Boundaries

- [ ] `kind` is optional in requests during compatibility transition. Missing `kind` means legacy direct chat.
- [ ] Direct requests require exactly one effective `agent_id`.
- [ ] Group requests require `agent_ids.length >= 2`.
- [ ] Group `agent_ids` must be deduplicated before validation.
- [ ] `orchestrator_agent_id` is required for group requests.
- [ ] `orchestrator_agent_id` must be one of `agent_ids`.
- [ ] All group agents must belong to the current workspace.
- [ ] Archived agents cannot be selected.
- [ ] Private-agent access rules must be applied to every selected agent, not only the Orchestrator.
- [ ] Group creation must run in one transaction: create session, insert participants, commit once.
- [ ] For group compatibility, `chat_session.agent_id` should store the Orchestrator agent ID until all legacy paths stop requiring a single agent.
- [ ] `chat_session.orchestrator_agent_id` is nullable for direct chats and required for group chats.
- [ ] Direct chats should not have an active participant with role `orchestrator`.
- [ ] Group chats must have exactly one active participant with role `orchestrator`.
- [ ] Group creation does not send the first message automatically.
- [ ] `mention_ids` must be structured JSON in the request. Do not parse `@name` from `content` in the backend.
- [ ] `mention_ids` must be validated against active participants in the current chat.
- [ ] Direct-chat send ignores `mention_ids` for routing but may keep them in message metadata later if UI needs display.
- [ ] System messages use `role='system'`, `message_type`, and `metadata`; do not create separate UI-only tables for system cards.
- [ ] `chat_message.message_type` defaults to `text` for old and new normal messages.
- [ ] `chat_message.metadata` defaults to `{}`.
- [ ] `ChatMessageResponse`, `packages/core/types/chat.ts`, and `ChatMessageSchema` must all include `agent_id`, `message_type`, and `metadata`.
- [ ] `CreateChatMessage` SQL must accept nullable `agent_id`, `message_type`, and `metadata`.
- [ ] Existing user and assistant messages must remain readable after the migration.
- [ ] WS `chat:message` payload must include `agent_id`, `message_type`, and `metadata` after this PR.

### Mention Routing

- [ ] Direct chat ignores mention routing and sends to the direct agent.
- [ ] Group with exactly one `mention_ids` entry sends directly to that agent.
- [ ] Group with zero `mention_ids` sends to Orchestrator.
- [ ] Group with two or more `mention_ids` sends to Orchestrator.
- [ ] `mention_ids` must be structured request data, not parsed from natural language text.
- [ ] Mentioned agent must be a current participant.

### PR5 Browser Console Fetch Requests

#### 1. Create direct chat with new contract

```js
const agentId = "replace-with-agent-id";
await apiFetch("/api/chat/sessions", {
  method: "POST",
  body: JSON.stringify({
    kind: "direct",
    agent_id: agentId,
    title: "Console PR5 direct",
  }),
});
```

Expected:
- Status `201`.
- Response has `kind: "direct"` or remains legacy-compatible with `agent_id`.
- IM list shows one participant.

#### 2. Create group chat

```js
const agentId = "replace-with-agent-id";
const agentId2 = "replace-with-second-agent-id";
const { body: groupSession } = await apiFetch("/api/chat/sessions", {
  method: "POST",
  body: JSON.stringify({
    kind: "group",
    agent_ids: [agentId, agentId2],
    orchestrator_agent_id: agentId,
    title: "Console PR5 group",
  }),
});
console.log("Group session:", groupSession?.id);
```

Expected:
- Status `201`.
- Response has `kind: "group"`.
- `orchestrator_agent_id` equals `{{agentId}}`.
- IM list shows two participants, one with role `orchestrator`.

#### 3. Reject group without Orchestrator

```js
const agentId = "replace-with-agent-id";
const agentId2 = "replace-with-second-agent-id";
await apiFetch("/api/chat/sessions", {
  method: "POST",
  body: JSON.stringify({
    kind: "group",
    agent_ids: [agentId, agentId2],
  }),
});
```

Expected:
- Status `400`.
- Error says Orchestrator is required.

#### 4. Send group message to one mentioned agent

```js
const groupSessionId = "replace-with-group-session-id";
const agentId2 = "replace-with-mentioned-agent-id";
await apiFetch(`/api/chat/sessions/${groupSessionId}/messages`, {
  method: "POST",
  body: JSON.stringify({
    content: "Please handle this part.",
    mention_ids: [agentId2],
  }),
});
```

Expected:
- Status `202` or current send-message success status.
- Enqueued task target is `{{agentId2}}`.
- Orchestrator is not invoked for this message.

#### 5. Send group message with zero mentions

```js
const groupSessionId = "replace-with-group-session-id";
await apiFetch(`/api/chat/sessions/${groupSessionId}/messages`, {
  method: "POST",
  body: JSON.stringify({
    content: "Plan the next steps.",
  }),
});
```

Expected:
- Status `202` or current send-message success status.
- Enqueued task target is the group Orchestrator.

#### 6. Send group message with multiple mentions

```js
const groupSessionId = "replace-with-group-session-id";
const agentId = "replace-with-agent-id";
const agentId2 = "replace-with-second-agent-id";
await apiFetch(`/api/chat/sessions/${groupSessionId}/messages`, {
  method: "POST",
  body: JSON.stringify({
    content: "Coordinate these agents.",
    mention_ids: [agentId, agentId2],
  }),
});
```

Expected:
- Status `202` or current send-message success status.
- Enqueued task target is the group Orchestrator.

---

## PR 6: Plan CLI & Step State

**Goal:** Let Orchestrator create a structured execution plan without natural-language parsing.

**Files:**
- Create: `server/migrations/101_chat_execution_plan.up.sql`
- Create: `server/migrations/101_chat_execution_plan.down.sql`
- Modify: `server/pkg/db/queries/chat.sql`
- Modify: `server/internal/handler/chat.go`
- Modify: `server/cmd/server/router.go`
- Modify: CLI command files under `server/cmd/multica`

### Required Behavior

- [ ] Create `chat_execution_plan`.
- [ ] Create `chat_execution_step`.
- [ ] Plan has `execution_mode` with `serial` default and `parallel` reserved.
- [ ] Step has `sequence`, `agent_id`, `planned_prompt`, `approved_prompt`, `status`, `task_id`, `base_revision`, `result_revision`, and `artifact_summary`.
- [ ] Orchestrator can add steps through structured API or CLI.
- [ ] Step creation validates target agent is a group participant.
- [ ] Submitted steps become `awaiting_approval`.
- [ ] The chat stream receives system cards for steps awaiting approval.

### PR6 Engineering Boundaries

- [ ] Plan APIs are structured JSON APIs. Do not accept natural-language plan text and parse it server-side.
- [ ] Only the group Orchestrator path can create or submit plans for that group chat.
- [ ] User-authenticated plan creation is allowed only for test/admin tooling if explicitly guarded; default product flow is Orchestrator task -> CLI/API -> plan.
- [ ] The server must verify `orchestrator_agent_id` equals the active Orchestrator participant for the chat.
- [ ] Each step `agent_id` must be an active participant in the same chat.
- [ ] A plan belongs to exactly one `chat_session_id`.
- [ ] A step belongs to exactly one plan and repeats `chat_session_id` for efficient locking/querying.
- [ ] Step `sequence` is unique per plan.
- [ ] Submitted plan creation and step creation must be transactional.
- [ ] If any step is invalid, no partial plan should remain unless the plan is explicitly kept as `draft`.
- [ ] v1 creates `awaiting_approval` steps after submit. It must not enqueue worker tasks during plan submission.
- [ ] `execution_mode='parallel'` can exist in schema but API should reject it or require explicit feature flag until parallel execution is implemented.
- [ ] Plan/step statuses must be documented as a finite state machine.
- [ ] Step system cards should be represented as `chat_message.role='system'`, `message_type='step_confirmation'`, and metadata containing `plan_id`, `step_id`, `sequence`, `agent_id`, and `planned_prompt`.
- [ ] Realtime events should invalidate plan/step queries or chat messages through React Query.
- [ ] CLI commands must send the same API payload shape that browser Console `fetch` validation can test.

### Plan/Step State Machine

Plan statuses:
- `draft`
- `awaiting_approval`
- `running`
- `completed`
- `cancelled`
- `failed`

Step statuses:
- `planned`
- `awaiting_approval`
- `queued`
- `running`
- `completed`
- `skipped`
- `cancelled`
- `failed`

Allowed step transitions:
- `planned -> awaiting_approval`
- `awaiting_approval -> queued`
- `awaiting_approval -> skipped`
- `awaiting_approval -> cancelled`
- `queued -> running`
- `running -> completed`
- `running -> failed`
- `queued -> cancelled`
- `running -> cancelled`

### PR6 Browser Console Fetch Requests

#### 1. Submit structured plan

```js
const groupSessionId = "replace-with-group-session-id";
const rootMessageId = "replace-with-root-message-id";
const orchestratorAgentId = "replace-with-orchestrator-agent-id";
const agentId2 = "replace-with-worker-agent-id";
await apiFetch("/api/chat/plans", {
  method: "POST",
  body: JSON.stringify({
    chat_session_id: groupSessionId,
    root_message_id: rootMessageId,
    orchestrator_agent_id: orchestratorAgentId,
    execution_mode: "serial",
    steps: [
      {
        agent_id: agentId2,
        planned_prompt: "Implement the first part.",
      },
    ],
  }),
});
```

Expected:
- Status `201`.
- Response includes `plan_id`.
- Created step status is `awaiting_approval`.
- Chat messages include a `step_confirmation` system card.

#### 2. Reject step targeting non-participant

```js
const groupSessionId = "replace-with-group-session-id";
const orchestratorAgentId = "replace-with-orchestrator-agent-id";
const nonParticipantAgentId = "replace-with-non-participant-agent-id";
await apiFetch("/api/chat/plans", {
  method: "POST",
  body: JSON.stringify({
    chat_session_id: groupSessionId,
    orchestrator_agent_id: orchestratorAgentId,
    execution_mode: "serial",
    steps: [
      {
        agent_id: nonParticipantAgentId,
        planned_prompt: "This should fail.",
      },
    ],
  }),
});
```

Expected:
- Status `400` or `403`.
- No runnable step is created.

---

## PR 7: Step Confirmation & Serial Lock

**Goal:** Require user confirmation before each planned agent step runs.

**Files:**
- Create: `packages/views/chat/components/step-confirmation-card.tsx`
- Modify: `packages/views/chat/components/chat-thread.tsx`
- Modify: `packages/core/chat/mutations.ts`
- Modify: `server/internal/handler/chat.go`
- Modify: `server/pkg/db/queries/chat.sql`

### Required Behavior

- [ ] Step confirmation card renders in the chat stream after Orchestrator output.
- [ ] User can continue a step.
- [ ] User can skip a step.
- [ ] User can edit `planned_prompt`; edited text becomes `approved_prompt`.
- [ ] Continuing a step enqueues exactly one agent task.
- [ ] Serial lock prevents another step in the same chat from running while one is `queued`, `dispatched`, or `running`.
- [ ] If lock fails, API returns `409 Conflict`.

### PR7 Engineering Boundaries

- [ ] Continue/skip APIs must validate the current user owns the chat session.
- [ ] Continue/skip APIs must validate the step belongs to the requested chat's current plan.
- [ ] Continue step must run in a database transaction.
- [ ] The transaction must lock the chat execution scope before checking running steps. Use either `SELECT ... FOR UPDATE` on the `chat_session` row or a Postgres advisory transaction lock keyed by `chat_session_id`.
- [ ] Inside the same lock, check no step in the chat is `queued`, `dispatched`, or `running`.
- [ ] Inside the same lock, update the step to `queued`, create the agent task, store `task_id`, and commit.
- [ ] Repeated continue on an already queued/running/completed step must be idempotent when safe or return `409` with a clear error.
- [ ] Skip is allowed only from `awaiting_approval`.
- [ ] Continue is allowed only from `awaiting_approval`.
- [ ] Editing prompt is allowed only before continue.
- [ ] Edited prompt must be stored in `approved_prompt`; never overwrite `planned_prompt`.
- [ ] Serial lock is per chat session, not global across all chats. Multiple conversations can run in parallel.
- [ ] The UI should disable continue/skip buttons while a mutation is pending.
- [ ] The UI must handle `409` by refetching the plan/steps and showing the current running step state.

### PR7 Browser Console Fetch Requests

#### 1. Continue step

```js
const stepId = "replace-with-step-id";
await apiFetch(`/api/chat/steps/${stepId}/continue`, {
  method: "POST",
  body: JSON.stringify({
    approved_prompt: "Implement the first part with these edits.",
  }),
});
```

Expected:
- Status `200`.
- Step status becomes `queued`.
- Step stores `approved_prompt`.
- Step stores `task_id`.
- Exactly one agent task is created.

#### 2. Skip step

```js
const stepId = "replace-with-step-id";
await apiFetch(`/api/chat/steps/${stepId}/skip`, { method: "POST" });
```

Expected:
- Status `200`.
- Step status becomes `skipped`.
- No agent task is created.

#### 3. Serial lock conflict

```js
const secondStepId = "replace-with-second-step-id";
await apiFetch(`/api/chat/steps/${secondStepId}/continue`, {
  method: "POST",
  body: JSON.stringify({
    approved_prompt: "Try to run while another step is running.",
  }),
});
```

Expected:
- Status `409` when another step in the same chat is `queued`, `dispatched`, or `running`.
- No second task is created.

---

## PR 8: Sandbox & Handoff

**Goal:** Keep each participant's agent memory isolated while sharing enough explicit group context.

**Files:**
- Modify: `server/internal/handler/task_lifecycle.go`
- Modify: `server/internal/handler/daemon.go`
- Modify: `server/pkg/db/queries/chat.sql`

### Required Behavior

- [ ] On task completion, update that participant's `chat_session_agents.session_id`.
- [ ] Direct legacy `chat_session.session_id`, `runtime_id`, and `work_dir` are still updated for compatibility.
- [ ] Handoff bundle includes the latest 20 group chat messages.
- [ ] Handoff bundle includes current plan summary.
- [ ] Handoff bundle includes previous step result summaries.
- [ ] Handoff bundle includes artifact card summaries.
- [ ] Handoff bundle includes `base_revision` and current workspace revision.
- [ ] Agent prompt explicitly tells the worker to read actual files instead of trusting summaries.
- [ ] Other agents' memory is passed through handoff bundle, not through shared `session_id`.

### PR8 Engineering Boundaries

- [ ] The system must be able to resolve a task back to its execution step. Prefer adding `agent_task_queue.chat_execution_step_id` or an equivalent non-ambiguous reference.
- [ ] `chat_execution_step.task_id` alone is not enough for daemon claim paths unless every claim/completion query can join task -> step efficiently.
- [ ] On task claim, daemon receives step context only for tasks linked to a chat execution step.
- [ ] On task completion, update participant `session_id` only for the agent that actually ran the task.
- [ ] Do not share one `session_id` among all group participants.
- [ ] Handoff bundle must include a bounded recent-chat window. v1 uses latest 20 messages.
- [ ] Handoff bundle must include current plan and completed step summaries, not raw unbounded transcripts.
- [ ] Handoff bundle should include artifact summaries but not full diffs.
- [ ] Worker prompt must state that summaries are guidance and actual files are authoritative.
- [ ] If workspace is a git repo, record `HEAD` and a dirty-tree hash or diff hash as revision markers.
- [ ] If workspace is not a git repo, record an empty revision or a best-effort snapshot marker; do not fail the step solely because revision tracking is unavailable.
- [ ] Revision capture failures should be stored as metadata warnings, not user-visible fatal errors unless task execution itself fails.
- [ ] Handoff bundle size must be bounded. If it exceeds the limit, truncate older messages first and mark `truncated: true`.
- [ ] Handoff bundle construction must not include secrets from environment variables.
- [ ] Handoff bundle should use workspace-relative paths only.
- [ ] Direct chat behavior must remain unchanged except for participant session tracking.

### PR8 Verification Requests

PR8 may not add a direct user-facing API, but verify through existing task APIs and daemon lifecycle:

#### 1. Claim a step-linked task

```js
const daemonToken = "replace-with-daemon-token";
const runtimeId = "replace-with-runtime-id";
const response = await fetch("/api/daemon/tasks/claim", {
  method: "POST",
  headers: {
    Authorization: `Bearer ${daemonToken}`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ runtime_id: runtimeId }),
});
console.log("daemon claim", response.status, await response.json().catch(() => null));
```

Expected:
- Status matches existing daemon claim success status.
- For a step-linked task, response includes or embeds handoff context.
- Handoff context includes latest messages, plan summary, previous step summaries, and revision fields.
- Handoff context does not include unbounded full history.

#### 2. Complete step-linked task

Use the existing daemon task completion endpoint for the claimed task.

Expected:
- Step status becomes `completed`.
- Participant row for the running agent updates `session_id`.
- Other participants' `session_id` values do not change.
- Step stores `result_revision` if available.

---

## PR 9: Artifact Cards

**Goal:** Show basic agent-produced artifacts inline in the chat stream.

**Files:**
- Create: `packages/views/chat/components/artifact-summary-card.tsx`
- Modify: `packages/views/chat/components/chat-thread.tsx`
- Modify: `server/internal/handler/task_lifecycle.go`
- Modify: `server/pkg/db/queries/chat.sql`

### Required Behavior

- [ ] On step completion, compute a basic artifact summary.
- [ ] Store summary in `chat_execution_step.artifact_summary`.
- [ ] Create a system chat message with `message_type='artifact_summary'`.
- [ ] Render `ArtifactSummaryCard` in chat stream.
- [ ] Card shows summary text.
- [ ] Card shows changed files if available.
- [ ] Card shows basic diff stat if available.
- [ ] Inline code diff remains deferred.

### PR9 Engineering Boundaries

- [ ] Artifact summary is best-effort. Failure to compute a summary should not fail an otherwise completed step.
- [ ] Store artifact summary in `chat_execution_step.artifact_summary` as JSONB.
- [ ] Also create a `chat_message` system row with `message_type='artifact_summary'` for chat-stream rendering.
- [ ] Artifact message metadata must be bounded in size.
- [ ] Show at most 20 changed files in the card.
- [ ] If more files changed, set `truncated: true` and include `total_changed_files`.
- [ ] File paths in metadata must be workspace-relative.
- [ ] Do not store absolute local paths in chat messages.
- [ ] Do not inline full diffs in PR9.
- [ ] `diff_stat` should be a short string or small object, not a raw `git diff`.
- [ ] Artifact card should handle empty changed file lists.
- [ ] Artifact card should handle unknown `change_type` with a generic file icon/state.
- [ ] Artifact summary generation should prefer git diff when available and fall back to known task output metadata when not.
- [ ] If both git and fallback metadata are unavailable, create a minimal card saying the task completed without detected file changes only when that is accurate.
- [ ] The UI must parse artifact metadata defensively through zod or a local safe parser before rendering.

### PR9 Browser Console Fetch Requests

#### 1. List messages after artifact creation

```js
const groupSessionId = "replace-with-group-session-id";
const { body: messages } = await apiFetch(`/api/chat/sessions/${groupSessionId}/messages`);
console.table(
  (messages ?? [])
    .filter((m) => m.message_type === "artifact_summary")
    .map((m) => ({
      id: m.id,
      role: m.role,
      message_type: m.message_type,
      changed_files: m.metadata?.changed_files?.length,
      truncated: m.metadata?.truncated,
    })),
);
```

Expected:
- Status `200`.
- Response includes a system message with `message_type: "artifact_summary"`.
- Artifact message `metadata.changed_files` has at most 20 items.
- Paths are workspace-relative.
- Metadata includes `truncated: true` if total changed files exceed the display limit.

#### 2. Artifact card survives malformed metadata

This is primarily a frontend/unit test case. If manually testing with a seeded malformed message:

Expected:
- Chat thread does not crash.
- Card falls back to a generic artifact/error state.
- Normal text messages still render.

---

## Realtime Events

Add these incrementally with the PR that first needs them:

| Event | Payload | PR |
|-------|---------|----|
| `chat:session_created` | `{ session_id, kind }` | PR5 |
| `chat:session_updated` | `{ session_id, title, updated_at }` | PR2/PR3 |
| `chat:session_deleted` | `{ session_id }` | PR2 |
| `chat:session_read` | `{ session_id }` | PR3 |
| `chat:message` | Include `agent_id`, `message_type`, `metadata` | PR5 |
| `chat:step_awaiting_approval` | `{ step_id, plan_id, agent_id, planned_prompt }` | PR6 |
| `chat:step_queued` | `{ step_id }` | PR7 |
| `chat:step_running` | `{ step_id }` | PR7 |
| `chat:step_completed` | `{ step_id, artifact_summary }` | PR9 |
| `chat:step_failed` | `{ step_id, error }` | PR7 |
| `chat:plan_cancelled` | `{ plan_id }` | PR7 |

Frontend handling belongs in `packages/core/realtime/use-realtime-sync.ts`, and it should invalidate TanStack Query keys rather than writing server data into Zustand.

---

## Verification Gate Before Local Deployment

Before deploying this plan locally after PR1-PR3:

- [ ] Run `make sqlc`.
- [ ] Run `pnpm typecheck`.
- [ ] Run `pnpm test`.
- [ ] Run `make test`.
- [ ] Start Web with `pnpm dev:web`.
- [ ] Manually verify `/lpc/chat`.
- [ ] Manually verify `/lpc/issues` has no Web floating chat.
- [ ] Manually verify the IM list does not crash when there are no sessions.
- [ ] Manually verify the IM list does not crash when there are archived sessions.
- [ ] Manually verify pin/archive APIs affect the IM list.

Before merging any PR in this sequence:

- [ ] Run `make check`.

---

## Deferred

- Inline code diff rendering.
- Webpage live preview cards.
- In-chat file editor.
- One-click deployment/publish flow.
- Token-aware chat summarization.
- Default parallel execution.
- Orchestrator replacement or transfer after group creation.
- Desktop chat-first shell.
- Human participants in group chats.
