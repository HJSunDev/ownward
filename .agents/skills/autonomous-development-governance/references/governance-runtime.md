# 自主治理运行时搭建规范

本规范用于把自主治理闭环落实为可恢复、可执行、可验证的项目能力。只搭建记录体系时无需读取本规范；只实现父子 Agent 通道时，同时读取 [Codex 主 Agent 与 Governor 子 Agent 交互规范](codex-governor-interaction.md)。

## 成立边界

运行时只解决两件事：跨上下文保存当前执行事实，以固定复核和事件复核防止局部执行失控。它不得代替产品需求、架构、开发规范、完成定义和关键工作留痕，也不得成为第二套任务管理或产品实现体系。

- 主 Agent 是唯一实现者。
- Governor 只读并独立复核，不修改产品。
- 确定性程序只维护状态、证据检查点、哈希、触发与校验，不判断开放问题。
- 只有真正的产品决策，或经事实确认无法自行取得的外部权限、凭证与状态，才暂停找用户；技术问题、路径失效、效率异常和仍可换路的资源问题由闭环自主处理。
- Codex Hooks 是触发与机械校验护栏，不是完整安全边界；主 Agent 必须同时遵守治理契约，安装时必须盘点实际工具路径并验证覆盖，不能假定所有工具都必然经过 Hook。

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
      ├─ events.jsonl
      ├─ review-request.json
      └─ reviews/
```

`config.json`、Schema、脚本、Agent 配置和 Hooks 随项目维护；`runtime/` 加入忽略规则，但在任务结束前不得随意清除。项目已有等价目录时复用，不建立平行体系。

建立新体系时从 Skill 的 `assets/governance-runtime/` 复制并适配 [状态 Schema](../assets/governance-runtime/state.schema.json)、[复核请求 Schema](../assets/governance-runtime/review-request.schema.json)、[复核结果 Schema](../assets/governance-runtime/review.schema.json)、[Governor 配置](../assets/governance-runtime/governor.toml)、[Hooks 模板](../assets/governance-runtime/hooks.template.json) 和 [运行配置模板](../assets/governance-runtime/config.template.json)。模板不绑定项目语言；`governance-cli`、Hook 命令和进程保护器使用项目已有运行时实现，必须满足下文命令契约与最小自检，不得为治理单独引入重量级依赖。

安装时必须替换 Hooks 命令和 `governed_tool_matcher` 占位符，按项目真实工具清单覆盖产品修改、外部写入以及批量、付费、不可逆和资源密集动作；不得用 `mcp__.*` 等宽泛模式替代实际工具清单。Bash 同时承载读写动作，无法仅靠 matcher 区分；Hook 适配器必须只解析输入便完成低成本纯读取的快速放行，不读取或写入治理状态，也不产生事件。项目本地 Hooks 需按 Codex 官方机制审阅并信任当前定义，任何定义变化后重新审阅；随后运行 `doctor` 和最小自检，不能以模板已复制、脚本直调或当前会话内测试代替全新会话的自动加载验证。

在 Codex 本地项目中，自定义 Agent 文件必须能够作为独立配置层通过校验。项目配置了 Governor 不需要的 MCP 时，必须盘点子 Agent 的实际继承结果；对于使用固定状态目录、不能安全启动第二实例的 MCP，应在 Governor 文件中写出同名 MCP 的完整 transport，再明确设置 `enabled = false`、`required = false`。不得只写开关，因为缺少 `command` 或 URL transport 的独立配置会成为无效 Agent 定义。具体已验证环境、故障链与冷启动判据见 [Codex 运行集成与冷启动验收](codex-runtime-integration.md)。

等待复核期间，Hook 适配器只能额外放行对固定 `governance-cli` 入口的精确治理控制调用；Shell 拼接、改写入口或任意同名命令不得借此绕过。`accept-review` 仍须在 CLI 内完成请求与结果 Schema、身份和哈希校验。

首次搭建按固定顺序完成：填充运行配置与主线入口提示词匹配规则，使用项目已有运行时实现 `governance-cli` 与 Hook 适配器，复制 Schema 和 Governor 配置，运行 `doctor`，再启用并信任 Hooks。状态不存在时，普通会话保持未激活；只有 `UserPromptSubmit` 命中主线入口提示词才原子执行 `init` 与首次 `resume`。治理状态存在后，`hook session-start` 才执行 `resume`；完成状态只返回完成事实，不得覆盖或伪造新任务。由此避免普通讨论和支线任务被主线治理误拦截，同时保证主线首次启动、恢复和完成后再次打开项目都有唯一行为。

## 单一状态权威

`runtime/state.json` 是当前执行状态的唯一权威，必须通过临时文件、落盘刷新和同目录原子替换更新。至少保存：

```json
{
  "schema_version": 1,
  "run_id": "...",
  "status": "running",
  "authority_hash": "sha256:...",
  "completion_conditions": [],
  "current_work_packet": {
    "packet_id": "...",
    "condition_id": "...",
    "objective": "...",
    "value": "...",
    "allowed_scope": [],
    "excluded_scope": [],
    "expected_evidence": [],
    "evidence_checkpoint": {
      "checkpoint_id": "...",
      "description": "...",
      "reached": false
    },
    "plan_hash": "sha256:...",
    "approval": {
      "status": "approved",
      "review_id": "...",
      "trigger_instance_id": "...",
      "review_snapshot_hash": "sha256:...",
      "valid_until_checkpoint": "..."
    },
    "started_at": "...",
    "last_evidence_at": "...",
    "checkpoint": "...",
    "failure_signatures": []
  },
  "pending_intervention": null,
  "explicit_resource_constraints": [],
  "reusable_results": [],
  "next_action": "...",
  "review": {
    "required": false,
    "review_id": null,
    "trigger_instance_id": null,
    "fixed_review_generation": 0,
    "review_snapshot_hash": null,
    "trigger": null,
    "decision_path": null
  }
}
```

约束如下：

- `authority_hash` 绑定本次任务采用的目标、权威依据和完成条件；权威内容变化后必须先重建状态，不能沿用旧判断。
- `expected_evidence` 必须写明可观测产物和验证方式；只有验证通过的新证据、已知缺口关闭或完成条件推进才算有效进展。
- `evidence_checkpoint` 是由工作结构决定的自然检查点，例如最小探测完成、首个失败复现、一个独立批次完成或一个模块验证完成；不得用预估开发时长代替。
- `reusable_results` 记录结果身份、适用范围、证据路径和输入哈希；不受当前变化影响的结果继续复用。
- `review_snapshot_hash` 只用于保证 Governor 返回前待复核快照没有变化；复核完成后由批准令牌绑定 `authority_hash`、工作包 `plan_hash`、允许范围和自然检查点。
- 工作包允许范围内为取得预期证据而发生的代码与证据变化不会使批准失效。权威依据、工作包目标、计划、允许范围或检查点改变，实际操作越界，或者进入新的固定复核代次时，批准立即失效。
- `explicit_resource_constraints` 只保存产品、用户、系统或外部服务已经明确规定的费用、调用量、存储、内存等客观边界；不得由 Agent 推测开发时间上限。
- 每次主任务 `SessionStart` 都必须原子递增 `fixed_review_generation` 并创建新的 `trigger_instance_id`；只有同一触发实例尚未完成的请求可以复用，既有结论即使状态哈希未变也不能替代本次固定复核。
- `pending_intervention` 是真实用户介入的唯一持久状态：绑定来源复核、输入类型、最小问题和安全解决摘要；暂停时保留当前工作包但撤销批准，用户只是追问时不得误解除暂停。密码、令牌、密钥及其他凭证原文不得进入状态、事件或复核材料。
- 关键工作留痕只保存长期关键结论，不复制实时状态和事件流水。

`events.jsonl` 只增记录状态转换、证据身份与摘要、归一化失败签名、资源边界事件、复核请求和结论；它用于审计与恢复，不是第二份状态。不得复制原始工具输出、终端流水、会话文本或可由引用产物读取的内容；需要保留的原始结果另存为证据文件，事件只记录路径和哈希。耗时可以作为 Governor 复核时的事实，但不能单独触发停止、否定路径或限制开发总时长。

## 证据与触发契约

开始产品工作前，主 Agent 必须用 `propose-work-packet` 绑定一项未完成条件，登记目标、价值、允许与排除范围、预期证据和由工作结构决定的下一证据检查点，并取得 Governor 批准令牌。

- 普通实现、调查和验证不需要预测工时，也不存在统一的任务、阶段或动作时长上限。
- 批量、付费、不可逆或资源密集动作先完成最小代表性探测，证明路径有效、结果可检查点化，并取得单元调用量、费用、存储或算力等客观事实；这些事实用于选择执行方式和提供复核依据，不形成开发截止时间。
- 探测规模只够判断路径和证据是否成立，不得先执行大批量工作再补证明。
- 结果可独立成立时立即形成检查点；后续只补失败或缺失部分，不能因批次部分失败丢弃有效结果或整批重做。

### 固定复核

主任务每次 `SessionStart` 都必须创建新的固定复核代次，包括 `startup`、`resume`、`clear` 和 `compact`；只有同一触发实例尚未完成的请求可以复用。在 Governor 结论通过确定性校验并落盘前，主 Agent 只能恢复事实、读取复核输入、启动并等待 Governor，不得继续产品实现、批量处理或正式验证。

固定复核必须让 Governor 直接读取目标、完成条件、仓库事实和原始证据；`state.json` 只负责指出当前声称的进度、路径和证据位置。状态与事实不一致时以事实为准，并局部失效错误状态。

Governor 是独立子会话。Codex 使用 `SessionStart`、`Stop` 管理主会话生命周期，使用独立的 `SubagentStart`、`SubagentStop` 管理子 Agent；治理模板不注册后两种事件，因此 Governor 启停不会创建新的固定复核。不得在 `governor.toml` 中关闭 Hooks；Governor 保持只读，其读取动作由现有低成本读取路径快速放行，其他安全、审计和项目 Hook 继续正常运行。

### 事件复核

以下情况必须额外请求复核：

1. 到达已登记的自然检查点，仍未产生预期证据，或证据推翻当前路径；
2. 同一失败签名在一次针对性修复后再次出现；
3. 当前工作包不再直接推进绑定的完成条件，或需要改变获准目标、范围、路径与检查点；
4. 最小代表性探测已经完成，准备扩大为批量、付费、不可逆或资源密集投入；
5. 实际费用、调用量或资源消耗将越过明确的外部约束；
6. 恢复时状态、代码、权威依据或证据哈希不一致；
7. 阶段结束或准备宣称全部完成。

主 Agent 与 Governor 的治理闭环是工作价值和效率的裁判。不得配置 `max_task_duration`、`max_phase_duration`、`max_action_duration` 或任何同义开发时长上限，也不得因为实际耗时超过预测而停止仍在持续产生必要证据的正确路径。

## 确定性命令契约

`governance-cli` 至少提供以下原子操作；命令名可以适配项目语言，但语义不得分叉：

| 操作 | 必要行为 |
| --- | --- |
| `init` | 校验权威依据与完成条件，创建唯一 `run_id` 和初始状态；已有有效状态时拒绝覆盖 |
| `resume` | 校验 Schema、哈希和运行目录，为本次 `SessionStart` 原子创建新的固定复核代次；只复用同一触发实例尚未完成的请求，复核完成前只输出复核动作 |
| `hook <event>` | 消费 Codex Hook 的标准输入并确定性映射：`session-start` 执行上述初始化或恢复；`pre-compact` 校验状态已原子落盘；`pre-tool-use` 对纯读取快速放行，否则校验工作包批准与范围；`post-tool-use` 只登记满足准入的状态、证据、失败或资源事件；`stop` 校验完成结论或生成复核请求 |
| `propose-work-packet` | 登记条件、目标、价值、允许与排除范围、预期证据和自然检查点并生成 `plan_hash`；未获 Governor 批准前不得执行产品修改 |
| `record-evidence` | 只机械校验证据文件、哈希、Schema 或验证器结果并登记身份；不得自行判断开放语义是否充分 |
| `record-failure` | 归一化失败签名并记录修复轮次 |
| `request-review` | 固化权威依据、工作包计划、仓库和证据索引的 `review_snapshot_hash`，通过复核请求 Schema 校验后原子生成唯一请求并阻止产品动作 |
| `resolve-intervention` | 校验待处理事项身份，只持久化准确、安全的解决摘要、来源 turn 和必要证据引用，原子生成新的 Governor 复核请求；不得自动复制用户原始消息或敏感信息 |
| `accept-review` | 校验 Schema、`review_id`、`review_snapshot_hash`、`trigger_instance_id` 和决策枚举，原样原子保存结果 |
| `apply-review` | `start` 创建首个工作包，`continue` 只刷新原工作包批准并保留全部连续性事实，`replan` 才换路，`stage_complete` 才进入下一阶段；真实用户输入只建立待处理事项，解决事实复核通过后才恢复运行 |
| `close-work-packet` | 只在自然检查点已到达、声明证据已机械登记且 Governor 完成检查点复核后关闭工作包 |
| `finish` | 只接受 `task_complete`，并机械验证该结论与当前权威、候选和完整证据集合绑定一致；`stage_complete` 不得结束整个任务 |
| `status --json` | 只读输出当前状态、证据检查点、复核状态和唯一下一动作 |
| `doctor` | 运行隔离自检，不接触产品资产和正式证据 |

所有写操作先持有单实例锁，再校验旧状态哈希并原子替换；并发写入、残缺 JSON 或哈希漂移必须失败关闭，不能自动覆盖。

确定性程序只能确认结构、身份、哈希、枚举、文件存在性和显式验证器结果；证据是否足以证明阶段或任务完成、当前路径是否最优等开放判断全部属于 Governor。

`governed-run` 只用于需要独立进程存活保护的外部命令，不限制正常开发时长。它依据命令自身提供、官方说明或最小探测确认的心跳、输出或检查点信号判断进程是否仍然存活；只有进程丢失既定存活信号时才终止完整进程树、保存已有结果并请求 Governor 复核。命令持续产生有效存活信号和必要证据时，无论总时长多久都不得因计时停止。普通读写、编辑和定向测试不经过该包装器。

## Hooks 职责

Hooks 只能执行确定性检查，不能自己充当 Governor，也不能直接启动子 Agent：

| 事件 | 行为 |
| --- | --- |
| `UserPromptSubmit` | 状态不存在时只对已配置的主线入口提示词原子执行 `init` 与首次 `resume`；存在待处理事项时向主 Agent 返回事项身份和处理契约，由主 Agent 区分追问与真实答复，Hook 不自行判断或保存原始消息；普通讨论和支线任务保持未激活 |
| `SessionStart` | 状态存在后，在 `startup`、`resume`、`clear` 和 `compact` 时调用 `resume`，创建本次固定复核代次并向主 Agent 提供唯一复核动作；状态不存在时不自行建立主线任务；有效结论落盘前不得继续产品工作 |
| `PreCompact` | 验证当前状态已经原子落盘；失败时保存错误并阻止把未持久化状态当成可恢复检查点 |
| `PreToolUse` | 对产品修改及批量、付费、不可逆、资源密集或需要存活保护的动作核对当前工作包批准令牌和允许范围；批准范围内的普通开发直接放行 |
| `PostToolUse` | 登记事件时间、退出状态、证据身份、资源事实和失败签名；触发条件成立时生成复核请求 |
| `Stop` | 只拦截主 Agent 在完成证据不足时主动宣称完成；不得阻止用户打断、产品决策暂停或资源异常停止 |

主线入口必须由 `UserPromptSubmit` 的项目特定匹配规则明确激活；匹配规则只识别持续开发主线，不得覆盖普通对话或支线。激活后，`SessionStart` 必须覆盖 `startup|resume|clear|compact`，为主任务原子创建新的固定复核代次；只允许复用同一触发实例未完成的请求。治理模板不得把 `SubagentStart` 或 `SubagentStop` 映射为固定复核，因而 Governor 启停不会形成递归。Hook 发现需复核时，必须原子创建请求、阻止产品动作，并向主 Agent 返回稳定的机器可识别原因。主 Agent 随后按父子交互规范启动 Governor、等待结果、校验落盘，再由 Hook 放行。Hook 不能靠自然语言猜测价值或证据，也不能根据经过时间自行裁决开发是否应当继续。

## Governor 配置

`.codex/agents/governor.toml` 必须限制为只读，并要求它只读取：

- 本次任务的目标、权威依据和完成条件；
- 当前 `state.json`、复核请求、相关证据、已发生的时间与资源事实和必要差异；
- 与触发原因直接相关的代码或执行结果。

Governor 配置不得关闭 Hooks。递归隔离依靠 Codex 主会话与子 Agent 生命周期事件的原生分离；治理模板不得注册 Governor 专用的 `SubagentStart` 或 `SubagentStop`。Governor 将 `state.json` 视为待验证声明，必须依据权威文档、仓库和原始证据独立形成判断；状态与事实冲突时不得迁就状态。

Governor 启动前还必须审计项目级 MCP：只保留复核确实需要且可以安全独立运行的服务。Governor 不需要且可能争用主任务状态目录的 MCP 必须在其独立配置中以完整 transport 明确禁用，不能依赖主任务当前进程、临时启动参数或不完整覆盖。

Governor 必须按 [Governor 宏观复核规范](governor-review.md) 先独立重建整体进度和最优下一工作包，再读取主 Agent 状态进行比较。它只能返回 `start`、`continue`、`replan`、`stage_complete`、`task_complete`、`product_decision_required` 或 `external_input_required`。`start` 只建立首个工作包，`continue` 只批准当前工作包；技术实现选择、普通失败、工具问题和可自主换路的效率问题不得升级给用户。复核请求包含已解决的待处理事项时，Governor 必须先判断输入是否充分，再恢复、换路或继续请求最小输入。具体请求、等待、返回与落盘协议见父子交互规范。

## 运行流程

1. **安装与激活**：确认权威层和完成定义充分并执行 `doctor`；由主线入口提示词首次命中时原子执行 `init` 与首次 `resume`，普通任务不建立状态。
2. **固定复核**：主线激活后，每次恢复、清空上下文或压缩后运行 `resume`，立即启动并等待 Governor；复核完成前不继续产品工作。
3. **恢复**：按 Governor 直接核对的仓库与证据事实恢复；若哈希漂移，先局部失效受影响状态，不丢弃无关有效结果。
4. **工作包**：用 `propose-work-packet` 绑定最关键的未完成条件，Governor 批准范围、证据和自然检查点后才执行；包内普通动作直接做，批量、付费、不可逆或资源密集动作先完成最小代表性探测；需要进程存活保护的外部命令才由 `governed-run` 执行。
5. **检查点**：证据一旦独立成立立即 `record-evidence`，只保存身份、哈希、验证器结果和适用范围；由 Governor 判断语义价值与充分性。
6. **事件复核**：触发后停止原动作，主 Agent 启动并等待 Governor；`accept-review` 成功前不得恢复投入。
7. **用户介入**：仅在真实产品决策或无法自行取得的外部输入时建立待处理事项并暂停；用户答复后由主 Agent 提交安全解决事实，Governor 复核通过后恢复原工作包或明确换路，整个过程可跨压缩恢复。
8. **换路**：`replan` 保留有效结果并关闭旧动作，再登记新的最小有效路径；禁止无关重跑。
9. **完成**：阶段结束使用 `stage_complete` 并直接进入下一工作包；只有同一成果满足整个完成定义时才允许 `task_complete`，此后 `finish` 才能成功。

## 最小自检

使用隔离夹具验证以下行为，不运行项目正式验收，也不修改产品：

- 普通讨论和支线提示词不会创建治理状态，配置的主线入口提示词会原子初始化唯一状态并进入首次固定复核；重复初始化不会覆盖；
- 主线激活后，恢复、清空上下文和压缩均会强制固定复核，复核完成前产品动作被阻止；
- 连续发生两次状态内容相同的启动或压缩时会形成两个不同的固定复核代次，前一次结论不能满足后一次；同一触发实例重入时不会重复创建请求；
- Governor 子会话不会递归触发新的 Governor；
- 治理 Hooks 不注册 `SubagentStart` 或 `SubagentStop`，Governor 配置中不存在全局关闭 Hooks 的选项；Governor 的读取动作快速放行，其他项目、安全和审计 Hook 仍会执行；
- 模拟压缩或重启后能够恢复检查点和唯一下一动作；
- Governor 能发现故意写入状态摘要、但与仓库或原始证据不一致的陈述；
- Governor 会先独立重建整体进度和最优下一工作包，再读取主 Agent 状态；对故意设置的无价值、低效或次优路径会返回根因和可直接执行的更优方案；
- 工作包范围内的正常代码与证据变化不会使批准令牌失效；改变目标、计划、范围或检查点以及越界动作会立即失效；
- 批量、付费、不可逆或资源密集动作未经最小代表性探测不能直接扩大；
- 状态、配置和 Hooks 不包含开发工时预估、时间预算或总时长硬截止；
- 工作持续产生预期证据时，不会因经过时间而触发停止或否定当前路径；
- 达到复核点且无新证据时会阻止原动作并生成唯一请求；
- Governor 的有效结果能够放行，错误 Schema、错误 ID 和旧状态哈希均被拒绝；
- 首次复核只能以 `start` 建立首个工作包；反复 `continue` 只更新批准令牌，开始时间、最近证据、检查点和失败历史保持不变；`replan` 与 `stage_complete` 才能建立新工作包；
- 真实用户介入会保留被暂停工作包并生成唯一事项；追问不解除暂停，有效答复经身份校验和 Governor 复核后恢复，答复后立即压缩仍可继续，过期事项被拒绝，治理产物不含敏感原文；
- 复核请求和复核结果分别通过固定 Schema 校验；缺失字段、额外字段、错误哈希或请求结果身份不一致均被拒绝；
- 待复核状态只放行固定治理入口的精确控制调用；Shell 拼接、入口改写和普通产品写操作仍被阻止；
- `governed-run` 在进程丢失既定存活信号时会结束完整进程树并保留检查点，不会自动重试；正常存活的长任务不受总时长限制；
- 部分有效批次在后续失败后仍可复用，只补缺失项；
- 普通低成本开发不被 Hooks 阻塞；
- Hooks 匹配器覆盖项目实际的修改与高风险工具且不使用全 MCP 通配；Bash 纯读取虽会进入适配器，但会在不访问治理状态、不写事件的情况下快速返回；已知不经过 Hook 的工具路径由主 Agent 契约约束并在自检中显式登记；
- 无既有状态的 `SessionStart` 保持未激活，主线 `UserPromptSubmit` 能够原子初始化并进入固定复核，运行中状态只恢复，完成状态不会被覆盖；Hook 模板的全部子命令都与 `hook <event>` 契约一致；
- 项目 Hooks 已按当前定义完成信任校验，定义变化后旧信任不会被误用；
- 全新 Codex 会话会自动执行已信任的 `SessionStart` 与 `UserPromptSubmit`，Governor 配置能够独立加载且不会为明确禁用的状态型项目 MCP 创建第二实例；脚本直调、同一会话热测和单次父子通道成功不能替代该冷启动证据；
- 事件流水不包含原始工具输出、终端流水或会话副本，只保存必要摘要和证据引用；
- `stage_complete` 必须携带下一工作包且不能结束任务；只有无剩余条件并绑定完整证据的 `task_complete` 能被 `finish` 接受；
- 确定性程序不能独立宣称语义证据充分或任务完成，最终判断必须绑定 Governor 结论；
- 完成证据不足时不能宣称完成，用户主动打断不会被 Hook 阻止；
- 清除隔离夹具后无进程、临时文件或测试状态残留。

全部自检真实通过，且由只读冷启动复核确认陌生执行者无需额外解释即可完成初始化、恢复、执行、复核和结束，治理运行时才算搭建完成。Skill 文字、配置文件存在或单次父子通道成功都不能单独作为完成证明。
