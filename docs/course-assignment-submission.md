# Agent Harness 作业提交说明

## 1. 代码仓库

项目：`go-nanoclaw`

核心代码位置：

- Main Loop：`internal/agent/agent.go`
- Provider 抽象：`internal/brain/brain.go`、`internal/brain/openai.go`、`internal/brain/anthropic.go`
- Tool Registry / 工具执行：`internal/hands/registry.go`、`internal/hands/hands.go`、`internal/hands/builtins.go`
- 本地演示 Brain：`internal/brain/scripted.go`
- CLI 演示入口：`cmd/nanoclaw/main.go` 的 `demo` 命令

## 2. 运行演示

无需 API key 的本地演示：

```bash
go build -o bin/nanoclaw ./cmd/nanoclaw
./bin/nanoclaw demo --task "读取 README 并总结项目能力" --read README.md
```

演示过程会经过真实的 agent loop：

1. 用户输入进入 `Agent.ProcessMessage`。
2. Agent 构建 request frame，包含 workspace bootstrap、历史消息和可用工具 schema。
3. `ScriptedBrain` 先输出 Thinking / Acting，并请求调用 `read_workspace_file`。
4. Tool Registry 通过统一入口执行工具，而不是在 demo 里直接读文件。
5. 工具结果作为 `tool_result` 注入上下文。
6. Agent 再次调用 Brain，得到最终总结。

示例日志结构：

```text
Agent.ProcessMessage
请求准备
请求帧
模型推理
工具循环 第1轮
  执行: read_workspace_file
  结果: ...
模型推理 第1轮
完成
```

## 3. 设计说明

这个项目把 Agent 拆成四层：`Agent` 负责 Main Loop，`Brain` 负责屏蔽不同大模型 Provider，`Hands` 负责工具注册、策略校验和执行，`Context/Memory` 负责把历史消息和 workspace 说明压成模型可用的 request frame。Main Loop 显式处理 Thinking 和 Acting：模型先返回文本推理和工具调用，Agent 执行工具后把 observation 作为 `tool_result` 放回上下文，再进入下一轮模型调用。Tool Registry 使用统一 schema 暴露工具，内置工具和自定义工具都经过同一个 `ExecuteStructured` 入口，因此后续增加新工具时不需要改 Main Loop。为了让作业可以稳定演示，我新增了 `ScriptedBrain` 和 `demo` 命令，它不依赖外部 API key，但仍然复用真实的工具分发、日志和上下文流转。

我的取舍是先保证 Level 1 的闭环完整：慢思考、通过 registry 调工具、拿到结果后再回答。项目里已有 `plan_execute` 和 `plan_execute_verify` 可以继续扩展到 Level 2/3，多步任务会被拆成 plan step 并持久化状态；这部分保留为进阶能力，没有为了演示去重写主循环。

## 4. 评分点对照

- 结构清晰：Main Loop、Provider、Tool Registry、Context 分层明确。
- 解耦：工具调用统一经过 `Hands.GetToolSchemas` 和 `Hands.ExecuteStructured`。
- 可扩展：新增工具只需要注册 schema 和 handler；新增 Provider 只需要实现 `brain.Brain`。
- 可演示：`nanoclaw demo` 在没有外部模型 key 的情况下仍能跑出 Thinking、Tool 调用和最终结果。
