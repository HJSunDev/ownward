# Codex 主 Agent 与 Governor 子 Agent 交互规范

本规范只说明 Codex 中的父子 Agent 交互。Governor 的判断质量见 [Governor 宏观复核规范](governor-review.md)。

## 职责

- 主 Agent 是唯一实现者，维护执行快照、启动复核、阅读反馈、明确回应并自行决定后续动作。
- Governor 使用独立上下文，只读目标、完成条件、仓库、状态和证据，返回风险与更优路线，不修改任何文件。
- 确定性运行时只识别自然边界、生成唯一请求、校验身份与 Schema、保存反馈和回应；它不判断语义，也不控制执行。

## 交互链路

Hook 不能直接启动智能体。合法触发成立后：

```text
Hook 创建或复用 review-request.json
        ↓
Hook 在自然边界向主 Agent 交付一次操作说明
        ↓
主 Agent 复用当前主任务唯一的 Governor；没有活动 Governor 时只启动一个
        ↓
主 Agent 等待 Governor 通过 Codex 原生父子通道返回一个 JSON
        ↓
主 Agent 调用 accept-review，确定性校验后原样落盘
        ↓
主 Agent 阅读反馈并调用 record-review-response
        ↓
主 Agent 按自己的判断继续
```

等待是为了在该自然边界取得完整反馈，不是冻结产品或夺取方向盘。Governor 无法启动、超时、返回错误或校验失败时，本次 Review 标记为 `missed`，主 Agent 继续；普通消息不得自动重试。

## 单一支线与幂等

同一主任务只维护一份治理状态、一个当前 Review 和一个 Governor 支线。主 Agent 在 Governor 可继续交互时复用它；一次性子 Agent 已自然结束时，新合法 Review 才重新启动同一角色。运行时以 `run_id + trigger kind + trigger type + source_id` 生成确定性触发身份：

- 同一目标模式提示重复投递使用完整提示内容哈希，复用已请求或已消费结果；
- 同一待处理生命周期边界重放不新增代次，前次复核结束后的下一次真实生命周期边界产生下一次复核；
- 当前 Review 未结束时不创建并行 Review；
- 普通用户消息、普通回复结束和 Governor 自身启停都不是触发。

## 请求与反馈

请求符合 [复核请求 Schema](../assets/governance-runtime/review-request.schema.json)，包括请求与触发身份、权威路径、仓库快照、当前执行焦点、证据引用、资源事实和待处理用户事项。状态摘要只是索引，Governor 必须直接核对权威依据、仓库和原始证据。

Governor 返回符合 [反馈 Schema](../assets/governance-runtime/review.schema.json) 的单个 JSON，带回相同 `review_id`、`trigger_instance_id` 和 `review_snapshot_hash`，并包含：

- 宏观进度与证据支撑；
- 最关键缺口；
- 当前路径的必要性、效率和最优性；
- 保留结果与建议失效项；
- 可选建议焦点或最小外部输入；
- `continue`、`adjust`、`stage_complete`、`goal_complete`、`product_decision_needed`、`external_input_needed` 之一。

`accept-review` 只校验 Schema、请求身份、快照哈希和证据引用，并保存原始反馈；不得据此自动更换焦点、失效证据、暂停任务或限制工具。`record-review-response` 保存主 Agent 的 `adopt`、`decline` 或 `acknowledge`、事实理由和下一验证点。

只有主 Agent 明确采纳真实产品决策或无法自行取得的外部输入建议时，才建立持久待处理事项。用户答复由主 Agent 以安全摘要和来源身份调用 `resolve-intervention`；密码、令牌、密钥等敏感原文不得进入治理材料。

## Hook 边界

- `UserPromptSubmit`：只对首行稳定标识或绑定交接令牌工作；其他消息严格沉默。
- `SessionStart`：只对治理所有者的 `startup`、`resume`、`clear`、`compact` 创建固定复核，并以 `additionalContext` 把操作说明交给主 Agent。
- `PreCompact`：尽力验证状态已落盘，失败也不阻止压缩。
- `PostToolUse`：只根据真实失败事实登记重复失败或 Governor 不可用；失败类别排除调用 ID、时间和耗时等易变元数据，不保存原始输出，也不返回阻断决定。
- 不注册 `PreToolUse`、`Stop`、`SubagentStart` 或 `SubagentStop`。

Hook 或运行时内部错误一律返回空结果并留下最小诊断；项目原有安全、权限与审计 Hook 继续独立生效。

## 已验证能力

Codex 原生父子通道能够让主 Agent 启动并等待 Governor、直接接收其 JSON，再由本地确定性程序校验落盘。该能力验证只证明交互通道可行；项目仍须通过自动测试和全新任务冷启动，证明 Hooks 自动加载、稳定标识、重复触发、普通消息隔离、Governor MCP 隔离和失败开放全部成立。
