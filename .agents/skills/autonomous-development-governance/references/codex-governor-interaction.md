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

主任务的首次明确激活、真实 `SessionStart` 恢复和规范主线提示词显式恢复属于固定触发；工作包、证据检查点、重复失败、权威变化、用户事项解决和显式完成属于事实事件复核；主动征求宏观建议属于 advisory。三类触发均携带结构化类型、来源身份和确定性实例 ID。通用 `Stop` 不注册为治理事件；Governor 的 `SubagentStart`、`SubagentStop` 也不注册，因此普通回复和 Governor 启停都不会递归复核。Governor 保持只读且不得通过 `hooks = false` 关闭全部 Hooks，其读取动作走低成本快速放行，其他安全、审计和项目 Hook 继续生效。

## 交互契约

复核请求必须符合 [复核请求 Schema](../assets/governance-runtime/review-request.schema.json)，其稳定字段包括：

- `review_id`、当前 `review_snapshot_hash` 与本次触发实例 `trigger_instance_id`；
- 结构化触发种类、受限类型、来源身份和仅用于说明的原因；
- 当前推进的完成条件；
- 当前工作包、允许范围、价值、预期证据和自然检查点；
- 已发生的时间、外部费用和资源消耗事实（仅在相关时提供，不要求预测开发工时）；
- 最近检查点和证据路径。
- 若任务因真实用户输入暂停，待处理事项的唯一身份、来源复核、最小问题及已经提交的安全解决摘要。

请求中的状态、结论和证据清单都只是索引。Governor 必须直接读取适用的目标与完成定义、检查仓库事实，并复核原始证据身份和内容；不得仅对状态摘要进行文字审阅。

Governor 只能返回结构化 JSON，并带回相同的 `review_id`、`review_snapshot_hash` 和 `trigger_instance_id`，同时返回宏观进度判断、最关键未满足条件、当前路径评价、保留结果和下一工作包。`decision` 只能是：

- `start`：当前没有工作包时，批准首个工作包；
- `continue`：当前路径不变，只刷新原工作包批准，不返回新工作包；
- `replan`：保留有效结果并换路；
- `stage_complete`：当前阶段证据充分但整个任务仍未完成，同时返回下一工作包；
- `task_complete`：整个完成定义已经由同一最终成果及完整证据满足；
- `product_decision_required`：存在真正需要用户决定的产品问题。
- `external_input_required`：经事实确认缺少无法自行取得的权限、凭证或外部状态。

`product_decision_required` 或 `external_input_required` 落盘后，主 Agent 停止产品工作并向用户提出最小问题。`UserPromptSubmit` 只把待处理事项身份和规则交还主 Agent：追问保持暂停，真实答复由主 Agent 通过 `resolve-intervention` 提交准确、安全的解决摘要与来源 turn，确定性程序校验身份并生成新复核请求。Governor 判断输入充分后才能返回 `continue`、`start` 或 `replan`；敏感原文不得进入状态或复核材料。该链路必须依赖持久状态而不是对话记忆，确保答复后发生压缩或恢复仍能继续。

## 确定性持久化

Governor 保持只读，不亲自写入监督文件。主 Agent 收到返回 JSON 后调用普通本地程序 `governance-cli accept-review`；程序只执行以下确定性检查：

1. JSON 符合固定 Schema；
2. `review_id` 与复核请求一致；
3. `review_snapshot_hash` 与当前待复核快照一致；
4. `trigger_instance_id` 与本次触发实例一致；
5. `decision` 属于允许值。

全部通过后，程序将原结果原子写入监督目录；任一检查失败则拒绝保存。复核返回前若目标、权威依据、工作包计划、仓库或证据快照已经变化，本次结果失效并重新复核。

有效结论落盘后，确定性程序生成绑定 `authority_hash`、工作包 `plan_hash`、允许范围和自然检查点的批准令牌。工作包范围内为取得预期证据而产生的正常代码与证据变化不会使批准失效；目标、权威依据、工作包目标、允许范围、路径或检查点变化，越出获准范围，或者固定复核代次变化时，批准令牌立即失效。主 Agent 收到的原生返回用于当次继续，落盘结果与批准令牌用于跨上下文恢复和 Hook 的后续机械校验。

批准代次与工作包身份必须分离：`continue` 仅替换批准令牌，原工作包的开始时间、最近证据、检查点和失败历史原样保留；只有 `replan`、`stage_complete` 或首次 `start` 能创建工作包。

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
