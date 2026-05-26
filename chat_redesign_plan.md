# Multica IM-first 多 Agent 聊天重构实施计划

本计划用于把 Multica 二次开发为一个以 IM 对话为核心交互范式的多 Agent 协作平台。用户像使用飞书或微信一样，通过新建单聊、群聊、发送消息、逐步确认 Agent 产出的方式，让不同 Agent 生成网页、Workflow、代码、文档等产物。

当前计划覆盖 Web first 的 chat-first 改造、单聊/群聊统一模型、Orchestrator 结构化分派、串行 step 确认、基础产物卡片、hybrid sandbox、旧协议兼容和分阶段 PR 顺序。

## 已确认边界

1. **Web 采用 IM-first 主界面**：工作区默认进入聊天页，页面布局为 `[会话列表 320px] [聊天主区域 flex-1]`。原 Issues、Projects、Agents、Runtimes、Settings 等入口收进抽屉，不再作为常驻左侧主导航。
2. **Desktop 暂不改**：Electron Desktop 继续使用现有 `ChatWindow` / `ChatFab` 浮窗。Web 的 chat-first 页面不能直接重写共享浮窗组件导致 Desktop 受影响。
3. **产品面向个人多 Agent IM**：群聊成员第一版只包含 Agent，不加入真人群成员、邀请入群、群权限等多人协作能力。现有 workspace/member 架构保留，UI 上弱化或隐藏多人入口。
4. **单聊和群聊统一模型**：单聊也是 `chat_session_agents` 中的一个 participant。旧的 `chat_session.agent_id/session_id/runtime_id` 保留兼容，不在本轮删除。
5. **群聊必须有 Orchestrator**：创建群聊时先选择多个 Agent，再从已选 Agent 中手动指定一个真实 Agent 作为 Orchestrator。没有 Orchestrator 的群聊不能创建或继续聊天。
6. **Orchestrator 是真实 Agent**：它有头像、消息、运行状态和该群聊内独立的 `session_id`。可以选择现有 Agent，也可以在建群流程中先创建新 Agent 再选择。
7. **路由规则**：群聊消息中 0 个 `@Agent` 时交给 Orchestrator 规划；1 个 `@Agent` 时直接交给对应 Agent；多个 `@Agent` 时交给 Orchestrator 规划。单聊只发给当前 Agent。
8. **执行策略**：第一版默认串行执行，不做真正并行。数据模型预留 `execution_mode` 和 `parallel_group_id`，后续支持并行。
9. **每一步都需要用户确认**：Orchestrator 生成结构化 plan 后，每个 worker step 在 enqueue 前都必须由用户确认。用户可编辑下一步任务内容后继续。
10. **Orchestrator 分派不解析自然语言**：可见回复只用于用户理解，真正分派必须通过结构化 CLI/API，例如 `multica chat plan add` / `submit`。
11. **Hybrid sandbox 保留并增强**：群聊共享一个 `work_dir`，每个 Agent 独立保存自己的 `session_id`。每个 step 记录 revision/checkpoint 和基础产物摘要，下一个 Agent 通过 handoff bundle 获取上下文。
12. **产物第一版先做基础卡片**：展示 task 状态、耗时、文件列表、附件、预览入口、transcript 入口、diff stat。完整代码 Diff 内联、网页实时预览、一键部署后续阶段实现。

## 非目标

- 第一版不做群聊真人成员、群权限、入群邀请。
- 第一版不做多窗口/多标签聊天；支持多个会话后台并行运行，但主界面一次只显示一个 active session。
- 第一版不做真正并行 Agent 执行；只预留数据字段。
- 第一版不做完整代码 patch 内联渲染、在线编辑器、一键部署。
- 第一版不迁移 Desktop 到 chat-first。
- 第一版不删除旧 chat DB 字段、不破坏旧 direct chat API。

## 目标体验

### Web Chat Shell

- 工作区默认路由：`/{workspaceSlug}/chat`。
- 桌面宽屏：
  - 左侧会话栏固定 320px。
  - 右侧为聊天主区域。
  - 原主导航入口放入菜单/抽屉。
- 移动端：
  - 默认显示会话列表。
  - 点击会话进入聊天详情。
  - 聊天详情左上角返回会话列表。

### 会话列表

- 支持新建单聊、新建群聊。
- 支持每个用户自己的置顶、归档、已读状态。
- 默认隐藏归档会话，底部提供“归档会话”入口。
- 支持搜索标题、参与 Agent 名称、最近消息内容。
- 排序规则：
  - 置顶在上。
  - 置顶内部按最近活跃排序。
  - 非置顶按最近活跃排序。
  - 最近活跃时间使用 `COALESCE(last_message_at, chat_session.updated_at)`。
- 会话行展示：
  - 单聊 Agent 头像或群聊头像组。
  - 标题。
  - 最近消息 preview。
  - 时间。
  - 未读、运行中、等待确认状态。

### 标题策略

- 单聊默认显示 Agent 名称。
- 群聊默认显示 Agent 名称组合，例如 `Codex, Claude Code, OpenCode`。
- `chat_session.title` 为空时前端 fallback 到参与者名称。
- 第一条用户消息后可用消息截断生成标题。
- 用户手动改名后不再自动覆盖。
- 可预留 `title_source`: `manual | first_message | generated | participant_fallback`。

## 数据库设计

所有 migration 必须是 additive。不要删除旧字段，避免破坏旧 Web/Desktop/安装客户端。

### chat_session 增量字段

```sql
ALTER TABLE chat_session
  ADD COLUMN kind TEXT NOT NULL DEFAULT 'direct'
    CHECK (kind IN ('direct', 'group')),
  ADD COLUMN title_source TEXT NOT NULL DEFAULT 'participant_fallback'
    CHECK (title_source IN ('manual', 'first_message', 'generated', 'participant_fallback')),
  ADD COLUMN execution_mode TEXT NOT NULL DEFAULT 'serial'
    CHECK (execution_mode IN ('serial', 'parallel'));
```

保留旧字段：

- `agent_id`
- `session_id`
- `runtime_id`
- `work_dir`
- `status`

兼容语义：

- 旧 direct chat 继续读取 `chat_session.agent_id/session_id/runtime_id`。
- 新 Web IM 页面优先读取 `chat_session_agents`。
- direct chat 创建时同时写旧字段和新 participant 表。

### chat_session_agents

```sql
CREATE TABLE chat_session_agents (
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('orchestrator', 'participant')),
  runtime_id UUID REFERENCES agent_runtime(id) ON DELETE SET NULL,
  session_id TEXT,
  last_seen_step_id UUID,
  last_seen_revision TEXT,
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  removed_at TIMESTAMPTZ,
  PRIMARY KEY (chat_session_id, agent_id)
);

CREATE INDEX idx_chat_session_agents_agent
  ON chat_session_agents(agent_id)
  WHERE removed_at IS NULL;
```

规则：

- direct 会话有且只有一个 active participant。
- group 会话必须有一个 active orchestrator，且 orchestrator 必须也是成员。
- 第一版不支持多个 orchestrator。
- `session_id` 是该 Agent 在该 chat session 内的 LLM resume pointer，不污染该 Agent 在其他会话中的记忆。
- `runtime_id` 用于 runtime guard。Agent 更换 runtime 后不能盲目 resume 旧 session。

### chat_session_user_state

用一个用户状态表统一置顶、归档、已读，取代单独的 pinned 表方案。

```sql
CREATE TABLE chat_session_user_state (
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
  workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
  pinned_at TIMESTAMPTZ,
  archived_at TIMESTAMPTZ,
  last_read_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (chat_session_id, user_id)
);

CREATE INDEX idx_chat_session_user_state_list
  ON chat_session_user_state(user_id, workspace_id, archived_at, pinned_at);
```

兼容语义：

- 新 IM 列表使用 `archived_at` 判断归档。
- 旧 `chat_session.status='archived'` 保留，只作为 legacy direct chat 的兼容来源。
- 第一版只有 creator 会使用 user state，但模型按用户维度设计，未来多人协作可扩展。

### chat_message 扩展

```sql
ALTER TABLE chat_message
  DROP CONSTRAINT IF EXISTS chat_message_role_check;

ALTER TABLE chat_message
  ADD CONSTRAINT chat_message_role_check
    CHECK (role IN ('user', 'assistant', 'system'));

ALTER TABLE chat_message
  ADD COLUMN agent_id UUID REFERENCES agent(id) ON DELETE SET NULL,
  ADD COLUMN message_type TEXT NOT NULL DEFAULT 'text',
  ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
```

第一版 `message_type`：

- `text`
- `plan_created`
- `step_confirmation`
- `artifact_summary`
- `plan_cancelled`
- `error`

消息语义：

- 用户消息：`role='user', message_type='text'`。
- Agent 回复：`role='assistant', agent_id=<agent>, message_type='text'`。
- Orchestrator 回复：同样是 assistant，通过 `chat_session_agents.role` 显示 Orchestrator 标识。
- step 确认卡片：`role='system', message_type='step_confirmation'`，metadata 保存 `plan_id`、`step_id`、`next_agent_id`、`planned_prompt`。
- 基础产物卡片：可以作为 assistant message metadata，也可以单独写 `message_type='artifact_summary'` 的 system/assistant message。

### chat_execution_plan

```sql
CREATE TABLE chat_execution_plan (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  root_message_id UUID REFERENCES chat_message(id) ON DELETE SET NULL,
  orchestrator_agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft', 'awaiting_approval', 'running', 'completed', 'cancelled', 'failed')),
  execution_mode TEXT NOT NULL DEFAULT 'serial'
    CHECK (execution_mode IN ('serial', 'parallel')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_chat_execution_plan_session
  ON chat_execution_plan(chat_session_id, created_at DESC);
```

### chat_execution_step

```sql
CREATE TABLE chat_execution_step (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  plan_id UUID NOT NULL REFERENCES chat_execution_plan(id) ON DELETE CASCADE,
  chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL,
  agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
  status TEXT NOT NULL DEFAULT 'planned'
    CHECK (status IN ('planned', 'awaiting_approval', 'queued', 'running', 'completed', 'skipped', 'cancelled', 'failed')),
  planned_prompt TEXT NOT NULL,
  approved_prompt TEXT,
  task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
  parent_step_id UUID REFERENCES chat_execution_step(id) ON DELETE SET NULL,
  parallel_group_id UUID,
  base_revision TEXT,
  result_revision TEXT,
  artifact_summary JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (plan_id, sequence)
);

CREATE INDEX idx_chat_execution_step_session_status
  ON chat_execution_step(chat_session_id, status, sequence);
```

重要规则：

- 不要把未来 step 直接插入 `agent_task_queue` 等待暂停。
- 只有用户点击“继续”后，后端才为该 step 创建 `agent_task_queue` row。
- 这样不需要给 `agent_task_queue.status` 增加 `paused`，避免破坏 claim、timeout、presence、analytics 等现有逻辑。

## 后端 API 计划

### 会话 API

新增或扩展：

- `GET /api/chat/sessions?view=im&q=&archived=false`
  - 新 Web IM 页面使用。
  - 返回 direct + group。
  - 包含 participants、orchestrator、last_message_preview、last_message_at、is_pinned、archived_at、kind、pending_state。
- `GET /api/chat/sessions`
  - 保留 legacy 语义，默认只保证旧 direct chat 兼容。
- `POST /api/chat/sessions`
  - 支持创建 direct/group。
  - direct payload: `kind=direct, agent_id`。
  - group payload: `kind=group, agent_ids[], orchestrator_agent_id`。
  - 可预留 `initial_message`，标准 UI 第一版可先创建空会话。
- `PATCH /api/chat/sessions/{id}`
  - 更新 title。
  - 用户手动改名时设置 `title_source='manual'`。
- `POST /api/chat/sessions/{id}/pin`
- `DELETE /api/chat/sessions/{id}/pin`
- `POST /api/chat/sessions/{id}/archive`
- `POST /api/chat/sessions/{id}/unarchive`
- `POST /api/chat/sessions/{id}/read`

后端校验：

- group 至少 2 个 Agent。
- group 必须提供 orchestrator。
- orchestrator 必须在 agent_ids 中。
- 所有 Agent 必须属于当前 workspace 且未 archived。
- 继续沿用 private-agent access gate。
- creator 必须是当前用户。第一版不做其他人访问该会话。

### 消息发送路由

`POST /api/chat/sessions/{id}/messages`：

1. 保存用户消息。
2. 根据会话 kind 和 `@Agent` 数量路由：
   - direct：创建当前 direct Agent 的 chat task。
   - group + 0 mention：创建 Orchestrator task，让它规划。
   - group + 1 mention：直接创建该 Agent 的 one-step task。
   - group + 多 mention：创建 Orchestrator task，让它规划。
3. `@Agent` 匹配必须基于结构化 mention 或后端可验证的 agent id/name，不依赖纯文本猜测。
4. 对 direct 中 mention 其他 Agent 的消息，第一版返回可操作错误或提示用户新建群聊。

### Step 确认 API

- `POST /api/chat/steps/{stepId}/continue`
  - body: `{ approved_prompt?: string }`
  - 校验 step 处于 `awaiting_approval`。
  - 写入 `approved_prompt`。
  - 创建 `agent_task_queue` row。
  - step 状态改为 `queued`。
- `POST /api/chat/steps/{stepId}/skip`
  - 当前 step 标记 `skipped`。
  - 下一个 step 进入 `awaiting_approval` 并写 system confirmation message。
- `POST /api/chat/plans/{planId}/cancel`
  - 未完成 step 标记 `cancelled`。
  - 写 `plan_cancelled` system message。
- 后续可加 `PATCH /api/chat/steps/{stepId}` 修改 prompt 草稿，第一版也可把修改放在 continue body。

### API Response Compatibility

必须补齐 chat 相关 schema：

- `ChatSessionSchema`
- `ChatParticipantSchema`
- `ChatMessageSchema`
- `ChatExecutionStepSchema`
- `PendingChatTaskSchema`

`packages/core/api/client.ts` 中 chat endpoints 不再裸 `fetch<T>`，改为 `parseWithFallback`。

兼容策略：

- 旧 direct chat 字段继续返回：`agent_id`、`status`、`has_unread`。
- 新字段 optional：`kind`、`participants`、`orchestrator_agent_id`、`last_message_preview`、`archived_at`、`is_pinned`。
- 新 Web IM 页面使用 `view=im`。
- Desktop 旧浮窗第一版只消费 direct 会话，避免未适配 group/system message 造成 UI 误读。

## CLI 与 Daemon 计划

### Orchestrator 结构化 plan CLI

新增最小命令集：

```bash
multica chat plan add --agent <agent-id-or-name> --prompt "..."
multica chat plan submit
multica chat plan clear
```

实现原则：

- CLI 只是当前 task 上下文的薄封装。
- 当前 `chat_session_id`、`task_id`、`workspace_id` 可通过 daemon 注入环境变量或 task context。
- 后端校验调用者 task 必须属于当前群聊的 Orchestrator step。
- `agent` 必须属于当前 `chat_session_agents` active members。
- submit 后创建 `chat_execution_plan` / `chat_execution_step`，并写 `plan_created` 和第一个 `step_confirmation` system message。
- 不解析 Orchestrator 的自然语言回复。

### Orchestrator Prompt

群聊无明确单一 `@Agent` 或多个 `@Agent` 时，Orchestrator prompt 需要包含：

- 当前群聊 ID。
- 当前用户消息。
- 群聊参与 Agent 列表：id、name、description、skills、runtime availability。
- 当前规则：必须用 `multica chat plan add` / `submit` 创建结构化计划。
- 可见回复要求：可以向用户说明分工，但执行以 CLI plan 为准。
- 最大 step 数，例如 8，防止无限递归。

### Worker Agent Prompt / Handoff Bundle

每次唤醒 worker Agent 时，daemon 构造 handoff bundle：

- 最近 20 条群聊消息，包含 sender、role、timestamp、content 或摘要。
- 当前用户请求。
- 当前 execution plan。
- 当前 step 的 approved prompt。
- 已完成 steps 的结果摘要。
- 上一步 Agent 的回复与基础产物卡片摘要。
- `base_revision` / 当前 shared work_dir revision。
- 明确要求：执行前读取真实文件，不要只依赖摘要。

对该 Agent 自己的历史：

- 优先使用 `chat_session_agents.session_id` resume。

对其他 Agent 的历史：

- 不共享它们的 LLM session。
- 只通过 handoff bundle 传递。

### session_id 更新

完成或失败 task 后：

- 如果是 direct legacy task，同步更新旧 `chat_session.session_id/runtime_id/work_dir`。
- 如果是新 group/direct participant task，更新 `chat_session_agents.session_id/runtime_id`。
- `work_dir` 仍存 chat_session 级别，作为共享目录。
- runtime mismatch 时不 resume 旧 session。

## Hybrid Sandbox 与产物

### 共享 work_dir

- 同一 chat session 共享一个 `work_dir`。
- 每个 step 开始前记录 `base_revision`。
- 每个 step 完成后记录 `result_revision`。
- 如果 work_dir 是 git repo，revision 优先使用 commit hash 或可验证 tree hash。
- 如果不是 git repo，生成内部 snapshot/diff id。

### 串行锁

第一版必须加逻辑锁，保证同一个 chat session 同一时间只有一个可写 worker step 运行。

原因：

- UI 串行不等于后端一定串行。
- retry、daemon recovery、重复点击都可能产生并发写入。
- 锁可以基于 DB 状态机和事务校验实现：同一 chat_session 不允许两个 step 同时进入 `queued/running`。

### 产物摘要

第一版基础产物卡片字段：

```json
{
  "summary": "Created initial React component shell",
  "changed_files": [
    { "path": "apps/web/...", "change_type": "modified" }
  ],
  "created_files": [],
  "deleted_files": [],
  "diff_stat": "3 files changed",
  "attachments": [],
  "preview_url": null,
  "transcript_task_id": "..."
}
```

注意：

- 不以 mtime 或 LLM 自述作为唯一事实来源。
- step 边界自动计算 changed files / diff stat / artifact metadata。
- 第一版不渲染完整 patch，但预留 `diff_ref` 或 `patch_artifact_id`。

## 前端实施计划

### Web-only Chat Shell

新增：

- `apps/web/app/[workspaceSlug]/chat/page.tsx`
- `packages/views/chat/components/chat-page.tsx`
- `packages/views/chat/components/chat-session-list.tsx`
- `packages/views/chat/components/chat-main-area.tsx`
- `packages/views/chat/components/chat-thread.tsx`
- `packages/views/chat/components/chat-composer.tsx`
- `packages/views/chat/components/chat-navigation-drawer.tsx`
- `packages/views/chat/components/new-direct-chat-dialog.tsx`
- `packages/views/chat/components/new-group-chat-dialog.tsx`
- `packages/views/chat/components/step-confirmation-card.tsx`
- `packages/views/chat/components/artifact-summary-card.tsx`

Web dashboard layout：

- 移除 Web 的 `ChatWindow` / `ChatFab` 挂载。
- Desktop layout 不动。
- 旧页面仍使用现有 `DashboardLayout` 或后续 Web layout；从 Chat drawer 可跳转。

### 抽屉导航

- Chat 页面提供菜单按钮。
- Drawer 内放原功能入口：
  - Inbox
  - Issues
  - Projects
  - Agents
  - Squads 可隐藏或降级为高级入口
  - Autopilots
  - Usage
  - Runtimes
  - Skills
  - Settings
- 不直接嵌入完整 `AppSidebar`，应抽取 nav item 定义或创建 drawer-friendly 列表。

### 会话状态管理

遵守仓库规则：

- React Query 管 server state：sessions、messages、participants、pending tasks、execution steps。
- Zustand 只管 client state：activeSessionId、搜索词、归档视图开关、草稿、移动端列表/聊天视图。
- Zustand store 放在 `packages/core/chat`，不放 views。

### 移动端

- `md` 以上显示双栏。
- `md` 以下：
  - 无 active session 时显示列表。
  - 有 active session 时显示聊天详情。
  - 聊天顶部提供返回按钮。

## 实时事件

新增或扩展 WS 事件：

- `chat:session_created`
- `chat:session_updated`
- `chat:session_deleted`
- `chat:message`
- `chat:done`
- `chat:step_awaiting_approval`
- `chat:step_queued`
- `chat:step_running`
- `chat:step_completed`
- `chat:step_failed`
- `chat:plan_cancelled`

缓存更新原则：

- WS 事件只更新或 invalidate React Query cache。
- 不把 server state 写进 Zustand。
- sessions list 需要响应 unread、pinned、archived、running、awaiting approval。

## 分阶段 PR 计划

### PR 1: Web Chat Route 与 IM Shell 骨架

目标：

- 新增 `/{workspaceSlug}/chat`。
- 新增 Web-only Chat Shell。
- 左栏 320px 会话列表骨架，右侧聊天空状态。
- 原导航进入 Drawer。
- `paths.workspace(slug).root()` 改到 `/chat`。
- proxy 根路径、登录后、创建工作区后默认跳转 `/chat`。
- Web 移除 `ChatWindow` / `ChatFab`，Desktop 不动。

验证：

- `pnpm typecheck`
- Web chat 路由可访问。
- Issues/Projects 等旧页面仍可从 drawer 进入。
- Desktop 编译不受影响。

### PR 2: Chat API 兼容 Schema 与会话列表基础

目标：

- 给 chat API 加 zod schema 和 fallback。
- 新增 `view=im` session list。
- 返回 `last_message_preview`、`last_message_at`、基础 participants shape。
- 前端会话列表接入 React Query。
- 搜索标题、Agent 名称、最近消息内容。

验证：

- chat API malformed response tests。
- 会话列表排序和搜索测试。

### PR 3: 用户维度置顶、归档、已读

目标：

- 新增 `chat_session_user_state`。
- pin/unpin、archive/unarchive、mark read API。
- 会话列表主视图隐藏归档。
- 底部“归档会话”入口。
- 置顶排序。

验证：

- Go handler tests。
- React Query optimistic update tests。

### PR 4: 统一 direct participant 模型

目标：

- 新增 `chat_session_agents`。
- direct chat 创建同时写旧字段和 participant row。
- 旧 direct sessions backfill 到 participant row。
- 新 Web direct chat 使用 participant。
- Desktop 旧浮窗仍能读旧字段。

验证：

- migration backfill test。
- direct chat send/receive regression tests。
- Desktop-facing legacy list 不出现未适配 group 会话。

### PR 5: Group Chat 创建与消息展示

目标：

- 创建群聊：选择多个 Agent，必须手动指定 Orchestrator。
- `chat_session.kind='group'`。
- 消息支持 agent_id、头像、Agent 名称、Orchestrator 标识。
- 群聊 0/1/多 mention routing 的基础分支：
  - 1 mention 直接 worker task。
  - 0 或多 mention 先 Orchestrator task。

验证：

- group create validation tests。
- mention routing tests。
- message render tests。

### PR 6: Orchestrator Plan CLI 与 Step 状态机

目标：

- 新增 `chat_execution_plan` / `chat_execution_step`。
- 新增最小 `multica chat plan add/submit/clear`。
- Orchestrator prompt 注入 Agent 列表和 plan CLI 使用规则。
- submit 后写 plan + steps + 第一个 step confirmation system message。
- 不解析自然语言分派。

验证：

- CLI command tests。
- Go API validation tests。
- Orchestrator submit plan 后不会自动 enqueue worker step。

### PR 7: Step 确认、编辑、串行执行

目标：

- 聊天流渲染 `step_confirmation` system card。
- 用户可编辑下一步 prompt。
- 继续后才 enqueue step。
- 支持跳过、终止计划。
- step 完成后写下一个 confirmation card。
- 同一 chat session 串行锁，防止两个 worker step 同时运行。

验证：

- step continue/skip/cancel handler tests。
- duplicate continue 并发测试。
- UI card interaction tests。

### PR 8: Hybrid Sandbox Checkpoint 与 Handoff Bundle

目标：

- `chat_session_agents.session_id/runtime_id` 更新路径。
- worker claim 时构造 handoff bundle。
- 注入最近 20 条群聊消息、plan、已完成 step、上一步结果、artifact summary。
- step 开始/结束记录 base/result revision。
- Agent prompt 明确要求读取真实文件。

验证：

- daemon claim payload tests。
- runtime mismatch 不 resume 测试。
- step revision 写入测试。

### PR 9: 基础产物卡片

目标：

- step completion 生成基础 `artifact_summary`。
- 聊天流展示 artifact summary card。
- 支持 changed files、created files、diff stat、attachments、transcript link。
- 预留 diff_ref / preview_url / deploy_ref。

验证：

- artifact metadata parse tests。
- UI render tests。

## 风险与应对

1. **旧客户端被 group/system message 破坏**
   - 用 `view=im` 给新 Web 页面返回新结构。
   - Desktop 第一版 direct-only。
   - 新字段 optional + zod fallback。

2. **Orchestrator 输出不可靠**
   - 不解析自然语言。
   - 只信 CLI plan submit。
   - 后端校验 agent membership、step count、session ownership。

3. **共享 work_dir 冲突**
   - 第一版强制串行锁。
   - step 记录 revision。
   - 后续并行通过 branch/worktree/merge step 实现。

4. **Agent 上下文混乱**
   - 每个 `chat_session_agent` 独立 session_id。
   - 其他 Agent 上下文只通过 handoff bundle 注入。
   - Orchestrator 可参与 step，但不默认加入；必须显式加入。

5. **一次性改造过大**
   - 按 PR 分层推进。
   - 前端 Shell、API schema、DB 模型、群聊、Orchestrator、sandbox、artifact 分开交付。

## 后续扩展

- 代码 Diff 内联。
- 网页预览卡片。
- 文件在线编辑。
- 一键部署发布。
- token-aware chat summarization。
- 群聊并行 step。
- Orchestrator 更换和移交。
- Desktop chat-first 适配。
- 从 Agent/Squad 模板快速创建群聊。

## 当前无阻塞疑问

本计划已纳入当前已确认的关键边界：Web IM Shell、Desktop 兼容、个人多 Agent 产品、群聊必须指定 Orchestrator、结构化 plan CLI、每步确认可编辑、hybrid sandbox、基础产物卡片优先、置顶/归档用户维度。

后续进入实现计划时仍需要细化具体 SQL 文件编号、API response 结构体字段名、CLI 命令参数命名、UI 文案和测试文件分布，但这些属于实现层细节，不阻塞本计划成立。
