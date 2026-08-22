# Codex 主 Agent 与 Governor 子 Agent 交互规范

本规范仅用于在 Codex 中建立、维护或审查自主开发治理的父子 Agent 交互。Governor 的判断顺序与质量标准见 [Governor 宏观复核规范](governor-review.md)。

## 职责边界

- **主 Agent** 是唯一产品实现者，负责维护执行状态、启动复核、等待结果并继续任务。
- **Governor** 使用独立上下文，只读目标、完成条件、状态、证据和实际投入，不修改产品或代替主 Agent 开发。执行状态只是待核对的主 Agent 陈述，不是 Governor 的事实权威；Governor 必须直接核对仓库和原始证据。
- **确定性看门狗** 只识别触发条件、阻止未获有效复核的动作并校验监督结果，不判断开放问题。

## 触发与等待

Hook 不能直接启动智能体。触发条件成立时，按以下固定链路执行：

```text
Hook 创建复核请求并阻止原动作
        ↓
主 Agent 启动 Governor
        ↓
主 Agent 等待，不继续原任务
        ↓
Governor 完成判断并通过 Codex 原生父子通道返回结果
        ↓
主 Agent 直接收到结果
        ↓
确定性程序校验并持久化
        ↓
看门狗验证有效后允许继续
```

对应的 Codex 执行关系是：

```text
父线程：spawn_agent(governor, 复核请求)
父线程：wait_agent(governor)
子线程：final(结构化 JSON)
父线程：直接收到 JSON
父线程：governance-cli accept-review(复核请求, JSON)
```

Skill 必须规定：复核触发后立即启动并等待 Governor；有效结论落盘前，原动作始终保持阻止。文件不负责通知子 Agent 已完成，实时交互由 Codex 原生父子通道承担。

主任务的首次启动、恢复、清空上下文和上下文压缩后复核属于固定触发；其他触发属于事件复核。Codex 以 `SessionStart`、`Stop` 管理主会话，以独立的 `SubagentStart`、`SubagentStop` 管理子 Agent；治理模板不注册后两种事件，因此 Governor 启停不会递归触发固定复核。Governor 保持只读且不得通过 `hooks = false` 关闭全部 Hooks，其读取动作走低成本快速放行，其他安全、审计和项目 Hook 继续生效。

## 交互契约

复核请求必须符合 [复核请求 Schema](../assets/governance-runtime/review-request.schema.json)，其稳定字段包括：

- `review_id`、当前 `review_snapshot_hash` 与本次触发实例 `trigger_instance_id`；
- 触发原因；
- 当前推进的完成条件；
- 当前工作包、允许范围、价值、预期证据和自然检查点；
- 已发生的时间、外部费用和资源消耗事实（仅在相关时提供，不要求预测开发工时）；
- 最近检查点和证据路径。

请求中的状态、结论和证据清单都只是索引。Governor 必须直接读取适用的目标与完成定义、检查仓库事实，并复核原始证据身份和内容；不得仅对状态摘要进行文字审阅。

Governor 只能返回结构化 JSON，并带回相同的 `review_id`、`review_snapshot_hash` 和 `trigger_instance_id`，同时返回宏观进度判断、最关键未满足条件、当前路径评价、保留结果和下一工作包。`decision` 只能是：

- `continue`：限定动作和下一证据检查点后继续；
- `replan`：保留有效结果并换路；
- `stage_complete`：当前阶段证据充分但整个任务仍未完成，同时返回下一工作包；
- `task_complete`：整个完成定义已经由同一最终成果及完整证据满足；
- `product_decision_required`：存在真正需要用户决定的产品问题。
- `external_input_required`：经事实确认缺少无法自行取得的权限、凭证或外部状态。

## 确定性持久化

Governor 保持只读，不亲自写入监督文件。主 Agent 收到返回 JSON 后调用普通本地程序 `governance-cli accept-review`；程序只执行以下确定性检查：

1. JSON 符合固定 Schema；
2. `review_id` 与复核请求一致；
3. `review_snapshot_hash` 与当前待复核快照一致；
4. `trigger_instance_id` 与本次触发实例一致；
5. `decision` 属于允许值。

全部通过后，程序将原结果原子写入监督目录；任一检查失败则拒绝保存。复核返回前若目标、权威依据、工作包计划、仓库或证据快照已经变化，本次结果失效并重新复核。

有效结论落盘后，确定性程序生成绑定 `authority_hash`、工作包 `plan_hash`、允许范围和自然检查点的批准令牌。工作包范围内为取得预期证据而产生的正常代码与证据变化不会使批准失效；目标、权威依据、工作包目标、允许范围、路径或检查点变化，越出获准范围，或者固定复核代次变化时，批准令牌立即失效。主 Agent 收到的原生返回用于当次继续，落盘结果与批准令牌用于跨上下文恢复和 Hook 的后续机械校验。

## 失败边界

- Governor 未返回、返回格式错误或校验失败时，原动作继续保持阻止，不得伪造通过。
- 主 Agent 不得改写 Governor 的语义结论，只能原样提交确定性校验。
- Governor 不得扩展产品范围、修改完成条件或把技术问题升级为产品决策。
- `external_input_required` 不得用于可以通过安全调查、实现调整或换路自行解决的问题。

## 已验证事实

2026-08-22 已完成一次隔离最小实测：主 Agent 创建请求、启动并等待子 Agent；子 Agent 通过 Codex 原生通道返回 JSON；主 Agent 直接收到结果；确定性脚本校验当时最小协议中的 `review_id`、`state_hash` 和决策枚举后原子落盘；主 Agent 重新读取结果并确认一致。该实测只证明父子返回与确定性持久化的核心链路可行，不证明本规范后来扩展的工作包、宏观判断和批准令牌契约已经实现，也不代表 Hooks 自动触发、上下文压缩恢复或长时进程看门狗已经验证。

## Codex 能力依据

- [Long-running work](https://learn.chatgpt.com/docs/long-running-work)
- [Subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents)
- [Hooks](https://learn.chatgpt.com/docs/hooks)
