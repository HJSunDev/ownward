# 自主治理运行时实际行为核对

> 核对基线：2026-08-23 自主治理运行时对齐完成后的同一最终版本。
> 本文只描述当前代码真实怎样运行；核心需求以 [`自主开发治理`](../../.agents/skills/autonomous-development-governance/SKILL.md) 为准。

## 直接结论

当前体系已经是“主 Agent 驾驶、Governor 顾问”的关系：本地治理程序只识别确定性事件、维护一份治理状态并生成复核请求；Governor 只读地判断整体进度、路径价值和执行效率，向主 Agent 提供风险与更优路线。主 Agent 必须阅读并明确回应反馈，但是否采纳、怎样执行以及何时继续始终由主 Agent 决定。

治理运行时不注册 `PreToolUse`、`Stop` 或子 Agent 生命周期 Hook，不拦截工具、不拒绝写入、不冻结或暂停主线，也不限制同一对话中的其他工作。Governor 或治理程序失败时，本次反馈记为缺失，主线继续；项目原有安全、权限和审计边界不受影响。

---

## 一、启动标识怎样工作

稳定启动标识是：

```text
[ownward-governance:enable]
```

它必须是提示词的第一个非空行，且整行精确匹配。`UserPromptSubmit` Hook 会在每条用户消息提交后运行一次确定性本地程序，但普通消息不匹配标识时只返回空 JSON：不创建 Review、不注入治理指令、不调用模型，也不改变治理状态。

首次命中标识时，程序执行：

```text
精确匹配启动标识
→ 创建或复用唯一 state.json
→ 绑定当前主任务所有权
→ 创建 activation Review 请求
→ 通过 additionalContext 通知主 Agent 发起 Governor 复核
```

目标模式重复投递同一提示词时，触发身份保持稳定，程序复用当前状态和 Review，不生成重复复核或第二条治理支线。启动标识只把治理顾问接入当前主目标，不把整个对话锁进治理模式；治理开启后，用户仍可安排任何其他工作。

对应配置：[config.json](../../.codex/governance/config.json)；对应实现：[hooks.go](../../.codex/governance/internal/governance/hooks.go)。

## 二、Hook 与 Governor 怎样协作

Hook 是固定的确定性程序，不具备语义判断能力，也不会自行调用模型。合法复核触发成立时，它只生成或复用 `review-request.json`，然后通过 `additionalContext` 通知主 Agent：复用当前主任务的单一 Governor，读取请求、返回一个 JSON，并保存反馈。

实际链路是：

```text
自然边界或真实治理事件成立
→ Hook 生成或复用 Review 请求
→ Hook 向主 Agent 提供操作说明
→ 主 Agent 复用 Governor；没有活动实例时才启动 governor
→ Governor 只读核对动态请求、权威依据、仓库和证据
→ Governor 返回一个符合 Schema 的 JSON
→ 主 Agent 用 accept-review 原样校验并保存
→ 主 Agent 阅读反馈并用 record-review-response 记录采纳、拒绝或确认
→ 主 Agent 按自己的判断继续工作
```

不存在“应用 Governor 决定”的控制步骤。兼容命令 `apply-review` 只记录主 Agent 已面对反馈，不授予许可、不修改执行方向，也不阻塞任何动作。

## 三、Governor 接收什么信息

Governor 输入由两部分组成：

- 固定角色契约位于 [governor.toml](../../.codex/agents/governor.toml)，要求它从宏观视角识别无价值工作、重复工作、范围扩张和低效率路径，并给出可以执行的更优建议；同时明确禁止实现产品、修改状态、批准或阻止动作。
- 动态请求位于 `runtime/review-request.json`，绑定当前权威依据、完成条件、仓库身份、执行快照、证据引用、检查点、资源事实、待处理用户事项和触发身份。

Governor 的 `recommendation` 只能作为建议进入主 Agent 的判断。即使 Governor 建议调整路线，主 Agent 也可以基于证据采纳、拒绝或确认后继续。

## 四、什么时候产生复核

复核只在自然边界或真实治理事件发生时产生：

1. 首次激活；
2. 已激活主任务的 `startup`、`resume`、`clear` 或 `compact` 生命周期边界；
3. 执行焦点或证据检查点发生真实变化；
4. 同类真实失败在一次已登记修复后再次出现；
5. 权威依据、证据身份或待处理用户事项发生真实变化；
6. 主 Agent 显式请求宏观建议或完成候选复核。

普通用户消息、普通回复、成功的普通工具调用和同一对话中的其他任务都不是治理事件。`PostToolUse` 只记录真实失败，并使用去除时间戳、调用 ID、耗时等易变信息后的稳定失败类别判断是否构成治理事件；它不做工具前置控制。

## 五、历史压缩后怎样恢复

`SessionStart` 是 Codex 的任务生命周期事件，不是新建主任务或 Governor。上下文压缩后的真实流程是：

```text
PreCompact 尽力确认状态可读
→ Codex 压缩主任务上下文
→ 原主任务收到 SessionStart(source=compact)
→ Hook 比较事件 session_id 与 state.json 的 owner.session_id
→ 只有治理主任务的生命周期事件可以创建复核
→ 相同待处理边界复用当前请求；新边界创建下一 Review 代次
→ additionalContext 把复核操作说明交给恢复后的主 Agent
```

所有权只用于识别哪条主任务生命周期可以创建复核，不是工作许可，也不限制其他任务。需要迁移到新对话时，`prepare-handoff`、`bind-handoff` 和交接标识只转移复核归属，不冻结源任务或目标任务。

## 六、单一治理状态与幂等性

- 每个主任务只维护一份 `state.json` 和一个当前 Review 槽位。
- 相同触发由稳定 `trigger_instance_id` 去重；重复投递只复用当前请求。
- 新事实使旧请求失效时，程序以新代次替换旧请求并记录 `review_superseded`，不会并行维护两个有效 Review。
- Governor 结果必须匹配当前 `review_id`、触发身份、执行快照、证据引用和检查点，过期结果不能回填新事实。
- Governor 子任务返回 JSON 后自然结束；治理状态在主 Agent 保存并回应反馈后进入 `responded`，Governor 不直接写状态。

## 七、失败与不可用时怎样处理

治理 Hook 和启动脚本均为 fail-open：解析、构建、状态或 Governor 链路失败时返回空 Hook 结果，不封锁 Codex。Governor 不可用时，当前 Review 可记为 `missed`，主线继续。

已经收到且校验通过的 `feedback_ready` 不能被后续失败降级为 `missed`；只有请求尚未得到反馈，或者反馈文件确实缺失、损坏时，才允许按不可用处理。这避免用一次失败绕过主 Agent 必须面对的有效反馈。

## 八、执行快照、证据与完成判定

执行快照只描述当前目标、价值、进展、涉及范围、预期证据和下一验证点，不包含 Governor 批准、执行许可或受控边界。

- 未变化的可复用证据可以绑定到后续执行快照。
- 新证据会使不能覆盖它的待处理 Review 失效并生成新请求。
- 已登记证据缺失或哈希改变时，旧绑定和相关完成结论立即失效，并生成证据身份变化复核。
- 检查点缺少预期证据时，不会伪装完成，而是记录缺口并创建顾问复核。
- 执行快照只有在检查点证据成立，且该边界的反馈已经回应或真实记为缺失后才能关闭。
- `finish` 会重新机械核验全部完成条件、证据身份和完成候选 Review；Governor 的 `goal_complete` 或其他建议不构成完成许可。

## 九、旧状态怎样迁移

旧版状态首次加载时自动迁移到 advisory v2：目标、价值、完成条件、进展、有效证据、检查点、失败事实、下一动作和所有权均保留；旧批准、许可、冻结、基础设施闩锁和旧请求退出活动链路，并归档到 `runtime/migrations/advisory-v2/` 供审计。

当前本地状态已经完成迁移，仍保持原主线 `run_3d5669569ce62500410764c4`、11 项完成条件、既有执行焦点与可复用证据。当前 Review 为 `missed`，表示该次顾问反馈不可用，不是阻塞或暂停；主线下一动作仍按迁移前的真实进度保留。

## 十、当前 Hook 集合

当前只注册四类 Hook：

- `UserPromptSubmit`：精确识别启动标识或合法交接标识；普通消息返回空结果。
- `SessionStart`：在已激活主任务的真实生命周期边界恢复治理复核。
- `PreCompact`：尽力确认状态已经安全持久化。
- `PostToolUse`：记录真实失败并在满足结构化条件时提出顾问复核。

不注册 `PreToolUse`、`Stop`、`PostCompact` 或任何子 Agent Hook。已经被 Codex 旧会话加载的 `pre-tool-use`、`stop`、`post-compact` 兼容调用也只返回空结果，不会拒绝或改写主线行为。

## 十一、已验证事实

同一最终版本已经通过：

- 治理模块全部 Go 测试、`go vet` 与构建；
- `doctor` 隔离自检，包括启动、普通消息隔离、重复触发、明确回应、压缩恢复和失败开放；
- 项目 Schema 与可复用 Skill 资产一致性检查；
- Hook 集合与禁止控制语义检查；
- 真实 Codex 冷启动，`SessionStart` 和普通 `UserPromptSubmit` 均正常完成，普通提示词没有触发 Governor；
- 最终只读 Governor 对抗式复审，结论为 `PASS`。

## 核对依据

- 核心规则：[SKILL.md](../../.agents/skills/autonomous-development-governance/SKILL.md)
- Hook 注册：[.codex/hooks.json](../../.codex/hooks.json)
- 触发配置：[config.json](../../.codex/governance/config.json)
- Hook 行为：[hooks.go](../../.codex/governance/internal/governance/hooks.go)
- 所有权与交接：[control_plane.go](../../.codex/governance/internal/governance/control_plane.go)
- Review、证据与完成生命周期：[runtime.go](../../.codex/governance/internal/governance/runtime.go)
- Governor 固定契约：[governor.toml](../../.codex/agents/governor.toml)
- 当前运行说明：[README.md](../../.codex/governance/README.md)
- 当前状态：`../../.codex/governance/runtime/state.json`（本地、不提交）
