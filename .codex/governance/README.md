# Ownward 自主治理运行时

本目录是开发治理能力，不属于 Ownward 产品运行时，也不会进入用户发布包。它以项目现有 Go 工具链实现确定性状态、检查点、Hook 校验和 Governor 结果持久化；唯一产品实现者仍是主 Agent。

## 激活与恢复

- 普通讨论和支线任务不会创建治理状态。
- 用户提交以“目标：持续完成 Ownward 第一版”开头的主线提示词时，`UserPromptSubmit` Hook 原子初始化运行状态并创建首次固定复核。
- 状态绑定唯一活动任务和单调所有权代次；同一任务的恢复与压缩可以继续，其他任务保持只读。跨任务恢复必须使用一次性交接标识原子转移所有权，不能靠新任务自动抢占。
- 活动任务首次启动、恢复、清空上下文和上下文压缩会创建新的固定复核代次；有效 Governor 结论落盘前，产品修改保持阻止。
- Governor 请求真实用户决策或外部输入时，运行时保留被暂停的工作包并建立唯一待处理事项。用户答复由主 Agent 通过 `resolve-intervention` 绑定到该事项，经 Governor 复核后恢复；追问不会误解除暂停，敏感原文不得进入治理状态。
- Governor 基础设施故障按 Review、失败签名和运行身份闩锁；同一故障不再由 Stop 或下一条消息自动重试，产品修改继续关闭，但任务可以保存状态并结束。控制面只能经 `repair-stage` / `repair-apply` 的冻结范围修复，新运行身份必须重新取得真实 Governor 结论。
- Governor、Explorer、Worker 与默认子 Agent 均由能力矩阵显式禁用 Ownward 产品 MCP；未登记角色不能依赖父任务继承获得用户数据面。
- 状态事务由操作系统持有的排他文件锁保护，进程异常退出会自动释放；磁盘上的 `.lock` 文件本身不代表锁仍被占用。
- `.codex/governance/runtime/` 与本地编译产物不提交；权威文档、Schema、CLI 源码、Governor 和 Hooks 随项目维护。

## 入口

Windows：

```powershell
.\.codex\governance\governance-hook.ps1 doctor
.\.codex\governance\governance-hook.ps1 status --json
```

macOS / Linux：

```sh
sh .codex/governance/governance-hook.sh doctor
sh .codex/governance/governance-hook.sh status --json
```

CLI 还提供 `init`、`resume`、`propose-work-packet`、`record-evidence`、`record-repair`、`request-review`、`resolve-intervention`、`accept-review`、`apply-review`、`prepare-handoff`、`bind-handoff`、`cancel-handoff`、`repair-stage`、`repair-apply`、`close-work-packet`、`finish` 与 `governed-run`。结构化输入默认从标准输入读取，也可用 `--file <path>` 提供。普通失败只能由 Hook 或 `governed-run` 按真实执行身份自动登记；需要立即复核时直接使用 `request-review`，不得重复上报同一失败。

失败是结构化事件，不是签名计数器。同一 `tool_use_id` 重放保持幂等；同一修复代次的重复失败只保留事实。`record-repair` 引用已验证事件和该事件发生后新登记的验证证据，仓库、候选、配置与实际运行程序身份由运行时自行计算并确认发生变化，调用者不能自报身份；只有此后同类失败再次真实发生才触发 `repeated-failure`。旧 `failure_signatures` 首次加载时迁移为永久不计数的 `legacy_unverified` 审计事实，旧待处理请求保留后由当前仓库快照的新请求取代。

`resolve-intervention` 接收 `intervention_id`、Hook 返回的 `source_turn_id`、准确且不含敏感原文的 `summary` 以及可为空的 `evidence_refs`；它只提交解决事实供 Governor 复核，不会自行解除暂停。

Governor 通过原生父子通道返回 JSON 后，不必建立中间文件：将其原样 Base64 编码并执行 `accept-review --json-base64 <base64>`，随后执行 `apply-review`。CLI 会验证固定 Schema、请求身份与快照哈希后才持久化。

任务迁移采用两阶段交接：活动任务以自身 Hook `session_id` 执行 `prepare-handoff`，Codex 返回新任务 ID 后执行 `bind-handoff`，再把命令返回的一次性标识放入新任务第一条 `UserPromptSubmit`。新任务消费后取得下一所有权代次；旧任务永久保持只读。标识不会写入事件或日志，超时和重复消费都会拒绝。

## Codex 一次性启用

项目级 Hooks 会由 Codex 自动发现，但非托管 Hook 的当前定义必须由用户在 Codex `/hooks` 中审阅并信任；定义变化后需要重新审阅。不得使用跳过 Hook 信任的启动参数代替这一步。官方依据：

- https://learn.chatgpt.com/docs/hooks
- https://learn.chatgpt.com/docs/agent-configuration/subagents
