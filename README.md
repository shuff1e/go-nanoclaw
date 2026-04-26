# go-nanoclaw

`go-nanoclaw` 是一个用 Go 实现的轻量级 Agent Runtime，面向单实例 / 小规模托管场景。项目目标是把一个可运行的 agent 原型逐步补齐到“可观测、可恢复、可控风险”的生产化 runtime，而不是接入外部 ADK。

## 当前能力

- 多入口：CLI chat、HTTP API、Discord bot。
- 模型 provider：Anthropic、OpenAI 兼容接口。
- 多 Agent：支持配置多个 agent，并通过 `delegate_task` 做受控委派。
- 工具调用：shell、文件、HTTP、memory、cron、自定义工具。
- 执行模式：`direct`、`plan_execute`、`plan_execute_verify`。
- 任务控制：异步任务、取消、手动重试、自动重试、SSE 状态流。
- 安全策略：工具 allowlist、文件路径策略、shell allowlist、HTTP allowlist、审批模式、API key、rate limit。
- 持久化：session、execution log、tool audit、approval record、trace、plan、cron、memory record。
- 可观测性：health、readiness、Prometheus metrics、runtime event、trace event、管理查询接口。

## 架构

```text
Entry points: CLI / HTTP / Discord
        |
        v
Control service
  |-- Request gate: API key, rate limit, task identity
  |-- Work queue: per agent/session serialization
  |-- Execution modes: direct, planned, planned+verify
  |-- Durable store: sessions, tasks, approvals, traces, plans, notes, schedules
  |-- Periodic checks and scheduled jobs
        |
        v
Execution pipeline
  |-- Request frame: workspace instructions, matched skills, conversation state
  |-- Model turn: Anthropic or OpenAI-compatible request
  |-- Policy-bound tools: allowlist, approval, audit, trace
  |-- Post-processing: persistence, context reduction, reflection
```

核心链路：

```text
input
 -> 创建 ExecutionContext(trace_id/request_id/session_id/task_id)
 -> 按 agent/session 进入串行队列
 -> 构建 request frame 并执行一个或多个 model turn
 -> 工具调用经过策略、审批、审计和 trace
 -> 持久化 execution/task/plan/session/memory/audit
 -> 返回同步结果或异步 task handle
```

## 快速开始

```bash
go build -o bin/nanoclaw ./cmd/nanoclaw

./bin/nanoclaw init

export ANTHROPIC_API_KEY="<your key>"
# 或 export OPENAI_API_KEY="<your key>"

./bin/nanoclaw chat --agent main
```

启动 HTTP 服务：

```bash
./bin/nanoclaw serve --host 127.0.0.1 --port 8765 --agent main
```

健康检查：

```bash
curl -s http://127.0.0.1:8765/health/ready
curl -s http://127.0.0.1:8765/metrics
```

## CLI

| 命令 | 说明 |
|---|---|
| `init` | 初始化 `~/.nanoclaw/config.yaml` 和 workspace 默认文件 |
| `chat` | 启动交互式聊天 |
| `serve` | 启动 HTTP 服务，可选同时启动 Discord |
| `discord` | 启动 Discord bot |
| `check` | 手动触发周期检查；`heartbeat` 作为兼容别名保留 |
| `status` | 查看配置和 agent 状态 |

常用参数：

- `chat --agent <id> --config <path>`：指定 agent 和配置文件。
- `serve --host <addr> --port <port> --agent <id>`：指定 HTTP 监听地址和默认 agent。
- `serve --discord-token <token>`：启动 HTTP 服务时同时启动 Discord channel；未传时读取 `DISCORD_BOT_TOKEN`。
- `discord --token <token> --agent <id>`：单独启动 Discord bot；未传 token 时读取 `DISCORD_BOT_TOKEN`。
- `check --agent <id>`：对指定 agent 手动触发周期检查。

`chat` 内置命令：

- `/quit`：退出聊天。
- `/plan <任务>`：使用 `plan_execute` 模式生成并执行结构化计划。
- `/verify <任务>`：使用 `plan_execute_verify` 模式执行计划，并在完成后追加质量门验证。
- `/skills`：列出当前 workspace 匹配到的 skills。
- `/agents`：列出已加载 agent。
- `/compact`：压缩当前会话上下文。
- `/clear`：清空当前会话上下文。
- `/check` 或 `/heartbeat`：手动触发周期检查。

示例：

```text
/plan Analyze the current project. Then propose improvements.
/verify Refactor the CLI plan command and verify the result.
```

全局日志参数：

- `-v`：输出 agent/control/tool 流程日志。
- `-vv`：输出更详细的 HTTP trace 日志，敏感 header 会脱敏。

## 配置

默认配置文件：`~/.nanoclaw/config.yaml`

```yaml
workspace: workspace
config_version: "local-dev"

api_keys:
  - "env:NANOCLAW_API_KEY"

rate_limit_per_minute: 120
max_context_chars: 100000
bootstrap_max_chars: 20000
max_wall_clock_seconds: 120
max_tool_rounds: 10
max_tool_calls: 32
max_tool_output_bytes: 20000

agents:
  main:
    workspace: ""
    brain:
      provider: anthropic
      model: claude-sonnet-4-20250514
      api_key: ""
      base_url: ""
      max_tokens: 4096
      temperature: 0.7

    heartbeat:
      enabled: true
      interval_minutes: 30
      active_hours_start: "08:00"
      active_hours_end: "23:00"

    cron: []

    allowed_tools:
      - "*"

    tool_policies:
      file_write_enabled: true
      file_write_allowlist:
        - "."
      shell_enabled: true
      shell_allowlist:
        - "git status"
        - "go test"
      http_enabled: true
      http_allowlist:
        - "api.github.com"
      approval_required:
        - "run_command"
      approval_timeout_minutes: 30

    subagents_allow:
      - "*"
    max_spawn_depth: 1
    max_subagents_per_task: 4
```

模型 key 解析顺序：

- `agents.<id>.brain.api_key`
- provider 默认环境变量：`ANTHROPIC_API_KEY` 或 `OPENAI_API_KEY`

HTTP 管理面 key：

- 配置 `api_keys` 后，受保护接口需要 `X-API-Key` 或 `Authorization: Bearer`。
- 未配置 `api_keys` 时，受保护接口不会强制鉴权；生产或共享环境应显式配置。
- `api_keys` 支持 `env:VAR_NAME`，推荐用环境变量，不要把真实 key 写进配置文件。
- `/health*` 和 `/metrics` 默认公开。

Rate limit：

- `rate_limit_per_minute` 只作用于非公开 HTTP 接口。
- 值为 `0` 或未配置时禁用限流。
- 限流身份优先使用 API key；没有 key 时按远端地址计数。

支持 provider：

| Provider | 默认模型 | 说明 |
|---|---|---|
| `anthropic` | `claude-sonnet-4-20250514` | Anthropic Claude |
| `openai` | `gpt-4o` | OpenAI 或兼容接口，可通过 `base_url` 指向兼容服务 |

## HTTP API

公开接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/health` | 兼容健康检查 |
| `GET` | `/health/live` | liveness |
| `GET` | `/health/ready` | readiness，检查控制服务和 store 可写性 |
| `GET` | `/metrics` | Prometheus 文本指标 |

受保护接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/input` | 同步执行一次输入 |
| `POST` | `/tasks` | 创建异步任务 |
| `GET` | `/tasks` | 查询任务视图、summary、plan |
| `GET` | `/tasks/stream` | SSE 订阅单个任务状态 |
| `POST` | `/tasks/cancel` | 取消运行中任务 |
| `POST` | `/tasks/retry` | 基于持久化 plan 重试任务 |
| `GET` | `/executions` | 查询 execution log |
| `GET` | `/tool-audit` | 查询工具审计，支持 `format=jsonl` |
| `GET` | `/approvals` | 查询待审批/已决策 approval record，支持 `format=jsonl` |
| `POST` | `/approvals/decide` | 记录 approval approve/reject 决策 |
| `GET` | `/traces` | 查询 trace event |
| `GET` | `/sessions` | 查询会话历史 |
| `GET` | `/memory` | 查询结构化 memory |
| `GET` | `/cron` | 查询 cron job |

同步输入：

```bash
curl -s -H "X-API-Key: <key>" \
  -d '{"text":"hello","agent_id":"main","session_id":"s1"}' \
  http://127.0.0.1:8765/input
```

异步计划任务：

```bash
curl -s -H "X-API-Key: <key>" \
  -d '{"text":"Analyze this. Then implement it.","agent_id":"main","mode":"plan_execute_verify","max_retries":1}' \
  http://127.0.0.1:8765/tasks
```

查询任务：

```bash
curl -s -H "X-API-Key: <key>" \
  "http://127.0.0.1:8765/tasks?agent_id=main&limit=20"
```

SSE 等待任务结束：

```bash
curl -N -H "X-API-Key: <key>" \
  "http://127.0.0.1:8765/tasks/stream?agent_id=main&request_id=req-..."
```

导出审计：

```bash
curl -s -H "X-API-Key: <key>" \
  "http://127.0.0.1:8765/tool-audit?agent_id=main&format=jsonl"

curl -s -H "X-API-Key: <key>" \
  "http://127.0.0.1:8765/approvals?agent_id=main&format=jsonl"
```

## 执行模式

| Mode | 行为 |
|---|---|
| `direct` | 直接执行一次 agent loop |
| `plan_execute` | 生成结构化计划，逐步执行并持久化 step 状态 |
| `plan_execute_verify` | 在计划执行后追加质量门验证 |

计划能力：

- CLI chat 可通过 `/plan <任务>` 和 `/verify <任务>` 触发计划模式；HTTP API 可通过请求体 `mode` 字段触发。
- step 包含 `id`、`title`、`prompt`、`depends_on`、`retry_policy`、`failure_strategy`、`checkpoint`。
- `depends_on` 会在执行前校验，依赖未完成会失败或按策略跳过。
- `retry_policy` 命中 transient retry 时会先做 step 级重试，再进入 task 级重试。
- `failure_strategy` 默认 `fail-plan`；`skip-step`、`continue`、`best-effort` 会把失败 step 标记为 `skipped` 并继续。
- `plan_execute_verify` 首次验证失败会追加一次 `replan-1` corrective step，再跑 `quality-gate-2`；第二次仍失败才终止。

异步任务状态：

- `queued`
- `started`
- `awaiting_approval`
- `completed`
- `failed`
- `cancelled`

自动重试只对 `timeout`、`tool_failed`、`brain_failed` 生效；不会重试参数错误、取消、审批等待、验证失败和权限拒绝。

## 工具与安全策略

内置工具：

| 工具 | 说明 |
|---|---|
| `run_command` | 按策略在 workspace 内运行命令并返回输出 |
| `read_workspace_file` | 读取通过路径校验的 workspace 文件 |
| `write_workspace_file` | 写入允许路径并按需创建目录 |
| `list_workspace` | 查看 workspace 目录的直接子项 |
| `fetch_url` | 获取 allowlist 允许的 HTTP(S) 内容 |
| `remember_note` | 写入长期记忆，可附带分类、置信度和过期时间 |
| `read_note` | 打开 workspace 中的记忆或说明文件 |
| `schedule_task` | 注册按时间触发的 agent 任务 |
| `list_schedules` | 查看已注册任务和最近运行状态 |
| `delegate_task` | 将边界清晰的子任务交给允许的 agent |

`cron` 和 `schedule_task` 的 `schedule` 支持两种格式：

- `HH:MM`：每天本地时间固定时刻运行。
- `*/N`：每 N 分钟运行一次。

安全控制：

- `allowed_tools` 控制 agent 能看到哪些工具。
- `file_write_allowlist` 限制可写路径。
- 文件路径会校验 workspace 逃逸和符号链接逃逸。
- `shell_allowlist` 限制可执行命令，命中 allowlist 时拒绝 shell 控制符拼接。
- `http_allowlist` 限制 HTTP 目标域名。
- HTTP 工具拦截 localhost、私网、link-local 地址，redirect 后会重新校验目标，并限制响应体读取大小。
- tool audit 会脱敏 token/password/secret/authorization/cookie 等敏感参数。
- approval record 会额外保存 redacted 参数副本，用于管理 API 和 JSONL 导出展示。

审批注意事项：

- `approval_required` 会拦截高风险工具，写入 pending approval record，把任务标记为 `awaiting_approval`。
- approve 后会执行原工具；reject 会把任务标记为 failed。
- `approval_timeout_minutes` 配置后，过期审批会记录为 `expired` 并失败任务。
- 为了 approve 后执行原工具，本地 store 会保留原始工具参数。不要把 API key、password、token 等 secret 放进待审批工具参数。

## 多 Agent

`delegate_task` 用于受控委派：

- `max_spawn_depth` 限制递归深度。
- `subagents_allow` 限制可委派的目标 agent。
- `max_subagents_per_task` 限制单个 execution 可创建的子任务数。
- 计划模式下，委派记录会写入 plan 的 `child_tasks`，包含目标 agent、prompt、状态、结果或错误。

## Memory

默认 workspace 文件：

| 文件 | 用途 |
|---|---|
| `SOUL.md` | workspace 运行规则 |
| `IDENTITY.md` | agent 身份 |
| `STARTUP.md` | 首次启动指令 |
| `HEARTBEAT.md` | 周期检查规则 |
| `CAPABILITIES.md` | 工具说明 |
| `MEMORY.md` | 长期记忆 |

`remember_note` 会同时更新 markdown 记忆和结构化 memory record。结构化 memory 支持：

- `category`：`profile`、`facts`、`preferences`、`notes`
- `source`
- `confidence`
- `expires_at`

组装上下文时会过滤过期 memory。

## 持久化

当前默认 store 是本地文件实现 `FSStore`，位置基于 `ConfigDir`，通常是 `~/.nanoclaw`。

持久化内容：

- `sessions/<agent>.jsonl`
- `executions/<agent>.jsonl`
- `tool-audit/<agent>.jsonl`
- `approvals/<agent>.jsonl`
- `traces/<agent>.jsonl`
- `plans/<agent>.jsonl`
- `cron/<agent>.json`
- `memory/<agent>.jsonl`

当前实现适合单实例或小规模内部部署。SQLite/Postgres、多实例共享数据库、分布式锁和外部队列还不是当前代码的一部分。

## 可观测性

- `/health/live`：进程 liveness。
- `/health/ready`：控制服务运行状态、store 可写性、config version、脱敏 config hash。
- `/metrics`：Prometheus 文本指标，包含请求数、错误数、运行任务、agent、cron、token、tool、task status、retry 统计。
- `/traces`：按 `trace_id`、`request_id`、`session_id`、`span`、`event`、`status` 和时间范围过滤 trace event。
- runtime event 覆盖 `thinking`、`planning`、`replanning`、`running_tool`、`verifying`、approval lifecycle 等阶段。

## Skills

技能文件放在 workspace 的 `skills/` 目录，使用 Markdown + YAML frontmatter：

```markdown
---
name: greeting
description: Respond to greetings
triggers:
  - type: keyword
    pattern: "hello"
tools: []
---

When the user greets you, respond warmly.
```

触发方式：

- `keyword`
- `regex`
- `always`

匹配到的技能会注入 agent 的系统提示。

## 项目结构

```text
go-nanoclaw/
├── cmd/nanoclaw/main.go
├── internal/
│   ├── agent/       # request pipeline、计划执行、子 agent
│   ├── brain/       # Anthropic / OpenAI-compatible model adapter
│   ├── channel/     # CLI / HTTP / Discord
│   ├── config/      # YAML 配置加载
│   ├── context/     # 请求窗口和 context reduction policy
│   ├── cron/        # 定时任务
│   ├── eval/        # 安全和行为回归 fixture
│   ├── gateway/     # 控制服务、任务、恢复、metrics、管理查询
│   ├── hands/       # policy-bound tools、安全策略、审计、trace
│   ├── heartbeat/   # 周期检查循环
│   ├── hooks/       # 事件总线
│   ├── memory/      # workspace bootstrap 和 memory
│   ├── runtime/     # ExecutionContext、dispatcher、错误分类、plan/event
│   ├── skills/      # skill 加载和匹配
│   └── store/       # Store 抽象和 FSStore
├── go.mod
└── go.sum
```

## 测试与验证

```bash
go test ./...
go test -race ./...
go build -o bin/nanoclaw ./cmd/nanoclaw
```

`internal/eval/fixtures` 保存回归 fixture，目前覆盖：

- shell 拼接拒绝
- HTTP 私网拒绝
- approval required 暂停
- 过期 memory 过滤
- 有效 memory 渲染

## 安全与合规检查

- 代码和文档只保留 Anthropic / OpenAI provider。
- 不包含已移除的高风险第三方 provider、配置或文档引用。
- README 使用占位符，不包含真实 key 形态示例。
- 配置推荐通过环境变量引用 API key。
- audit 和 approval 管理面默认返回 redacted 参数。

## 当前边界

- 默认 store 是本地文件，不是多实例共享数据库。
- approval approve 会执行原工具，但不是跨进程持久运行中的完整 workflow engine。
- HTTP streaming 当前是任务状态 SSE，不是 token-by-token LLM streaming。
- token 估算仍基于字符数，不是 provider tokenizer。
- 当前目标是单租户 / 小规模内部托管，不是完整 SaaS 多租户平台。
