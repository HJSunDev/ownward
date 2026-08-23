# 自主治理运行时搭建规范

本规范用于把自主治理闭环落实为可恢复、可执行、可验证的项目能力。只搭建记录体系时无需读取；只实现父子 Agent 通道时，同时读取 [Codex 主 Agent 与 Governor 子 Agent 交互规范](codex-governor-interaction.md)。

## 成立边界

运行时只负责保存执行事实，并在自然边界取得独立宏观反馈。它不是第二套产品实现、任务审批或权限体系。

- 主 Agent 是唯一实现者，始终掌握执行决定。
- Governor 只读，负责发现无价值、低效或次优路径并提出更优方案。
- 确定性程序只维护状态、身份、哈希、触发、校验和事件，不判断开放问题。
- Governor 反馈必须由主 Agent 阅读并明确回应，但不构成批准、许可或强制命令。
- Governor、Review 和治理 Hook 不得拦截工具、拒绝写入、冻结、暂停、接管主线或限制同一对话中的其他工作。
- Governor 或治理运行时失败时记录本次反馈缺失并放行主线；不得关闭项目原有安全、权限和审计机制。

## 项目资产

```text
.codex/
├─ agents/
│  └─ governor.toml
├─ hooks.json
└─ governance/
   ├─ config.json
   ├─ state.schema.json
   ├─ review-request.schema.json
   ├─ review.schema.json
   ├─ governance-cli.*
   ├─ governed-run.*
   └─ runtime/                 # 本地运行状态，不提交
      ├─ state.json
      ├─ review-request.json
      ├─ review.json
      └─ migrations/
```

配置、Schema、脚本、Agent 和 Hooks 随项目维护；`runtime/` 忽略提交，但任务结束前不得随意清除。已有等价目录时复用，不建立平行状态。

新项目从 Skill 的 `assets/governance-runtime/` 复制并适配 [状态 Schema](../assets/governance-runtime/state.schema.json)、[复核请求 Schema](../assets/governance-runtime/review-request.schema.json)、[反馈 Schema](../assets/governance-runtime/review.schema.json)、[Governor 配置](../assets/governance-runtime/governor.toml)、[Hooks 模板](../assets/governance-runtime/hooks.template.json) 和 [运行配置模板](../assets/governance-runtime/config.template.json)。运行程序使用项目已有轻量工具链，不为治理引入重量级依赖。

## 激活与单一治理支线

`config.json` 必须定义一行稳定、精确的 `activation_marker`。持续开发入口把它作为第一个非空行；`UserPromptSubmit` 只做等值匹配，不用自然语言前缀或正则猜测任务类型。

- 没有状态时，只有精确标识原子创建状态和首次复核。
- 已有状态时，同一完整提示重复投递使用其内容哈希作为触发身份，复用已有请求或已经消费的结果；目标模式重复提示不得新建复核。
- 普通消息、普通回复和同一对话中的其他工作始终返回空 Hook 结果，不修改状态、不重现旧反馈。
- 同一主任务只有一份 `state.json` 和一个当前 Review 槽位；主 Agent 复用同一 Governor 支线，同一触发不能生成第二个请求或并发 Governor。
- 新的合法自然边界可以生成后续 Review，但不能把整个对话锁进治理模式。

## 执行状态

`runtime/state.json` 是当前执行事实的唯一权威，通过单实例锁和原子替换更新。最小结构如下：

```json
{
  "schema_version": 2,
  "run_id": "...",
  "status": "active",
  "authority_hash": "sha256:...",
  "completion_conditions": [],
  "current_focus": {
    "focus_id": "...",
    "condition_id": "...",
    "objective": "...",
    "value": "...",
    "involved_scope": [],
    "expected_evidence": [],
    "evidence_checkpoint": {
      "checkpoint_id": "...",
      "description": "...",
      "reached": false
    },
    "snapshot_hash": "sha256:..."
  },
  "pending_intervention": null,
  "reusable_results": [],
  "next_action": "...",
  "review": {
    "status": "idle",
    "fixed_review_generation": 0
  },
  "owner": null,
  "handoff": null
}
```

`current_focus` 只是主 Agent 的执行快照，描述当前目标、价值、进展、涉及范围、预期证据和下一自然验证点；它不包含允许范围、排除范围、批准令牌或执行许可。Governor 的建议不得直接改写它，只有主 Agent 的显式状态操作可以更新。

### 当前态持久化

治理运行时只维护三个当前态文件，不保存日常历史：

- `state.json` 是唯一执行事实，保存当前目标、进展、证据、检查点、所有权、Review 状态和主 Agent 回应；每次变更原子覆盖。
- `review-request.json` 是当前 Governor 输入，绑定请求身份、快照、证据与检查点；产生新请求时原子覆盖。
- `review.json` 是当前 Governor 原始输出；通过 Schema、请求身份和快照校验后原子覆盖。

生命周期固定为：

```text
产生新请求
→ 原子覆盖 review-request.json，并使上一份 review.json 失效
→ Governor 返回后校验并原子覆盖 review.json
→ 主 Agent 阅读反馈，将回应和下一验证点写入 state.json
→ 下一次合法复核重复这一过程
```

运行时不维护历史 Review 目录或只增事件日志，也不通过历史重放恢复状态。恢复所需事实必须直接存在于 `state.json`，当前请求和当前反馈分别由两个固定文件承载；失败、修复代次和证据身份等仍影响执行的事实属于当前状态或证据，不得依赖历史流水。一次性 Schema 迁移只使用 `migrations/` 中的明确标记保证幂等，不以日常事件历史充当迁移状态。

Governor 反馈是当前决策输入，不是长期记忆。新请求产生后，旧请求和旧反馈已经被当前状态吸收，不再保留；只有满足关键工作留痕准入条件的长期结论，才由项目既有记录体系独立保存。不得为治理运行时增加轮转、压缩摘要、历史归档或另一套记忆机制。

## 复核触发

### 固定复核

主任务首次激活，以及来源为 `startup`、`resume`、`clear`、`compact` 的真实 `SessionStart`，在动作之间的自然边界创建复核；其中 `compact` 通过 Codex 正式支持的 `additionalContext` 在压缩后的下一次模型请求前交付操作说明。生命周期事件没有独立事件 ID，因此当前请求仍待处理时的重放复用该请求；前次复核结束后的下一次真实生命周期事件创建新代次。

`PreCompact` 只做尽力而为的状态完整性检查，不得阻止压缩。`Stop`、`PreToolUse`、`PostCompact`、`SubagentStart` 和 `SubagentStop` 不得注册为治理 Hook：回复结束不是任务结束，工具调用也不是治理许可边界，而 `PostCompact` 的 `systemMessage` 只用于界面提示，不能替代模型可见的复核指令。失败工具事件只保存必要身份、原始证据哈希、排除调用 ID、时间和耗时等易变元数据后的失败类别哈希及验证事实，不复制终端流水或敏感原文。

### 事件复核

以下真实事实形成事件复核：

1. 执行焦点发生实质变化；
2. 到达证据检查点，证据不足或推翻当前路径；
3. 同类失败在一次有新证据的针对性修复后再次发生；
4. 权威依据或证据身份发生变化；
5. 最小代表性探测完成，准备扩大批量、付费、不可逆或资源密集投入；
6. 用户待处理事项得到绑定身份的解决；
7. 阶段结束或准备宣称完成。

开放语义由 Governor 判断；确定性程序只识别已登记事实。主 Agent 也可用稳定 `request-id` 主动请求 advisory。不得用经过时间、工时预测或自由文本理由伪造固定或事实事件。

## 反馈与回应

Hook 不能启动子 Agent。合法触发只生成或复用 `review-request.json`，向主 Agent 交付一次操作说明：

```text
自然边界产生请求
→ 主 Agent 复用单一 Governor
→ Governor 只读独立复核并返回一个 JSON
→ accept-review 校验 Schema、请求身份和快照哈希后原子覆盖 review.json
→ 主 Agent 阅读反馈并用 record-review-response 明确采纳、拒绝或确认
→ 主 Agent 按自己的判断继续
```

Governor 返回 `continue`、`adjust`、`stage_complete`、`goal_complete`、`product_decision_needed` 或 `external_input_needed`。建议焦点、建议失效项和用户输入建议都只是反馈；`accept-review` 不能改变执行焦点、完成条件、工具权限或任务状态。请求必须绑定当时可见的证据集合和检查点；证据新增、缺失或身份变化会使旧请求失效，不能由旧反馈补认。检查点反馈已经回应，或 Governor 不可用已经记为 `missed` 后，主 Agent 才关闭该执行快照。

主 Agent 的回应必须记录处置、事实理由和下一验证点。只有主 Agent 明确采纳真实产品决策或无法自行取得的外部输入建议时，才建立持久 `pending_intervention`；普通技术问题和可自主换路的效率问题不得交给用户。

Governor 启动、执行、返回或校验失败且尚未形成可用反馈时，将当前 Review 标记为 `missed` 并保留执行快照与证据；已经进入 `feedback_ready` 的有效反馈必须保留到主 Agent 明确回应，不能被后续链路失败降级为缺失。主线始终可继续，下一次新的自然边界可以重新取得反馈；同一失败不能由普通消息无限重试。

## 命令契约

| 操作 | 必要行为 |
| --- | --- |
| `init` | 创建唯一运行状态；已有状态时拒绝覆盖 |
| `hook <event>` | 处理自然边界；任何治理内部错误都以空结果失败开放 |
| `update-execution-snapshot` | 由主 Agent 更新描述性执行快照并形成焦点变化事件 |
| `record-evidence` | 校验文件、哈希和显式验证器结果，登记可复用证据 |
| `record-repair` | 绑定真实失败、修复后新证据和新的执行身份，推进修复代次 |
| `request-advisory-review` | 用稳定 `request-id` 创建主动宏观复核 |
| `request-completion-review` | 绑定当前权威和完整证据创建完成候选复核 |
| `resolve-intervention` | 只保存准确、安全的解决摘要和来源身份，生成新复核 |
| `accept-review` | 校验并原子覆盖当前 `review.json`，不应用为控制 |
| `record-review-response` | 保存主 Agent 的采纳、拒绝或确认及下一验证点 |
| `apply-review` | 仅兼容已加载旧指令，等价于不改变执行状态的确认 |
| `complete-execution-snapshot` | 由主 Agent 在自然检查点完成后关闭当前快照 |
| `finish` | 机械核对全部完成条件证据及已回应或记为缺失的完成复核后结束；Governor 的建议类型不是完成许可 |
| `status` | 只读输出当前状态 |
| `doctor` | 使用隔离状态执行确定性自检，不接触产品资产和正式证据 |

所有写操作持有单实例锁并原子替换；并发写入、残缺 JSON、错误身份或快照漂移应拒绝该治理写入，但不能通过 Hook 拒绝产品动作。

## 进程与资源

批量、付费、不可逆或资源密集动作先用最小代表性样本确认路径、检查点化能力和客观资源事实。独立有效结果即时保存，后续只补失败或缺失部分。

`governed-run` 只保护确实需要存活检测的外部进程：丢失其已知心跳时结束进程树并保存已有结果；持续产生有效心跳时不设总时长上限。普通开发、编辑和定向测试不经过它。

## 所有权与交接

所有权只决定哪个主任务的生命周期事件可以创建复核，不决定谁能工作。其他任务和同一对话的其他工作不受限制。两阶段一次性交接只转移后续复核归属；准备、绑定和消费交接均不得冻结源任务或目标任务。

## 旧状态迁移

升级审批型旧状态时必须原子完成：

1. 归档旧 `state.json`、活动请求和专用迁移标记；
2. 保留运行身份、完成条件、执行目标、价值、进展、证据、检查点、失败事实、所有者和交接；
3. 将旧工作包转换为 `current_focus`，把旧允许与排除描述仅作为涉及范围事实保存；
4. 删除活动批准、许可、冻结、基础设施闩锁和阻断状态；
5. 仍待主 Agent 回应且身份有效的当前反馈迁移到固定 `review.json`；其余历史 Review 和事件流水不进入新运行目录；
6. 迁移可重入，完成后只由新 Schema 和明确迁移标记加载，正常运行不继续增加迁移文件。

## 最小自检与冷启动

实现至少验证：

- 普通消息不激活、不注入、不改状态；稳定标识只激活一次，目标模式重放幂等；
- 激活、恢复、压缩和各真实事件只在自然边界创建一次请求；
- Governor 反馈不能修改执行快照，主 Agent 回应可持久恢复；
- Governor 失败、错误 JSON、旧哈希和治理 Hook 异常不会阻止主线；
- Hooks 不含 `PreToolUse`、`Stop` 和子 Agent 生命周期事件，兼容旧调用严格空操作；
- 旧状态迁移保留有效进度与证据，活动控制语义全部失效；
- Governor 配置只读，项目状态型 MCP 在 Governor 中以完整 transport 明确禁用；
- 配置、项目 Schema 与可复用资产一致，运行目录只有三个覆盖更新的当前态文件，不生成历史 Review 或事件流水；
- 全新 Codex 任务会自动加载当前 Hooks：无标识普通消息正常，带标识首次激活只交付一次顾问复核，Governor 能返回且失败时主线仍可继续。

确定性自检、自动测试和全新任务冷启动均通过后，运行时才算搭建完成。脚本直调或单次父子通道成功不能替代真实冷启动。
