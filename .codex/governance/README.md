# Ownward 自主治理运行时

本目录是开发治理能力，不属于 Ownward 产品运行时，也不会进入用户发布包。它以项目现有 Go 工具链实现确定性状态、检查点、Hook 校验和 Governor 结果持久化；唯一产品实现者仍是主 Agent。

## 激活与恢复

- 普通讨论和支线任务不会创建治理状态。
- 用户提交以“目标：持续完成 Ownward 第一版”开头的主线提示词时，`UserPromptSubmit` Hook 原子初始化运行状态并创建首次固定复核。
- 状态存在后，首次启动、恢复、清空上下文和上下文压缩都会创建新的固定复核代次；有效 Governor 结论落盘前，产品修改保持阻止。
- Governor 请求真实用户决策或外部输入时，运行时保留被暂停的工作包并建立唯一待处理事项。用户答复由主 Agent 通过 `resolve-intervention` 绑定到该事项，经 Governor 复核后恢复；追问不会误解除暂停，敏感原文不得进入治理状态。
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

CLI 还提供 `init`、`resume`、`propose-work-packet`、`record-evidence`、`record-failure`、`request-review`、`resolve-intervention`、`accept-review`、`apply-review`、`close-work-packet`、`finish` 与 `governed-run`。结构化输入默认从标准输入读取，也可用 `--file <path>` 提供。

`resolve-intervention` 接收 `intervention_id`、Hook 返回的 `source_turn_id`、准确且不含敏感原文的 `summary` 以及可为空的 `evidence_refs`；它只提交解决事实供 Governor 复核，不会自行解除暂停。

Governor 通过原生父子通道返回 JSON 后，不必建立中间文件：将其原样 Base64 编码并执行 `accept-review --json-base64 <base64>`，随后执行 `apply-review`。CLI 会验证固定 Schema、请求身份与快照哈希后才持久化。

## Codex 一次性启用

项目级 Hooks 会由 Codex 自动发现，但非托管 Hook 的当前定义必须由用户在 Codex `/hooks` 中审阅并信任；定义变化后需要重新审阅。不得使用跳过 Hook 信任的启动参数代替这一步。官方依据：

- https://learn.chatgpt.com/docs/hooks
- https://learn.chatgpt.com/docs/agent-configuration/subagents
