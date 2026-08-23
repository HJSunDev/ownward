# Ownward 自主治理运行时

本目录承载 Ownward 开发阶段的自主治理能力，不属于产品运行时，也不会进入用户发布包。主 Agent 始终是唯一实现者；Governor 只提供独立、只读的宏观复核和更优路线建议。

## 工作关系

- `state.json` 保存当前完成条件、执行快照、证据、下一验证点、复核反馈与主 Agent 回应；执行快照不是批准、许可或受控范围。
- `review-request.json` 和 `review.json` 分别保存当前 Governor 请求与当前原始反馈；三个当前态文件均先校验再原子覆盖，不保留日常历史流水。
- Governor 反馈必须被主 Agent 阅读并明确回应，但是否采纳及如何执行始终由主 Agent 决定。
- Governor、Review 和治理 Hook 不拦截工具、不拒绝写入、不冻结或暂停任务，也不限制同一对话中的其他工作。
- Governor 或治理运行时失败时，本次反馈标记为缺失，主线继续；项目原有安全、权限和审计机制保持不变。

## 激活、复核与恢复

- 启动标识是 `[ownward-governance:enable]`，且必须是提示词第一个非空行。它只把 Governor 接入当前主目标。
- 普通消息、普通回复和同一对话中的其他工作不创建复核、不注入治理指令、不改变治理状态。
- 目标模式重复投递相同提示时复用既有触发；同一主任务始终使用一份状态和一个 Governor 支线。
- 首次激活以及真实 `startup`、`resume`、`clear`、`compact` 边界由 `SessionStart` 创建复核并把操作说明交给恢复后的主 Agent；同一待处理边界重放复用当前请求，前次复核结束后的下一次生命周期事件创建新代次。
- `PostToolUse` 只以必要身份、原始证据哈希和去除易变元数据后的失败类别哈希记录真实失败，并在符合结构化事件条件时创建复核；`PreCompact` 只做尽力而为的状态检查。
- 未变化的可复用证据会自动绑定到后续执行快照；证据新增会替换无法覆盖它的待处理旧请求，证据文件缺失或哈希变化时，旧绑定和完成结论立即失效并生成顾问复核。执行快照在反馈已回应或记为缺失后关闭，`finish` 重新机械核验全部证据身份，但不把 Governor 的建议当成完成许可。
- 治理不注册 `PreToolUse`、`Stop` 或子 Agent 生命周期 Hook。已加载旧配置产生的兼容调用严格返回空结果。
- 所有权只决定哪个主任务的生命周期事件可以创建复核，不赋予工作许可，也不限制其他任务。交接只转移该复核归属。

旧版状态首次加载时自动迁移：有效目标、价值、进展、证据、检查点、失败事实和所有权被保留；活动批准、许可、冻结、基础设施闩锁和旧请求退出活动链路并归档到 `runtime/migrations/advisory-v2/`。历史 Review 与追加事件流升级时只提取仍然有效的当前请求、反馈和触发身份，写入三个当前态文件后清理；迁移幂等只由 `runtime/migrations/current-state-v1/` 的明确标记保证。

## 入口

Windows：

```powershell
.\.codex\governance\governance-hook.ps1 doctor
.\.codex\governance\governance-hook.ps1 status
```

macOS / Linux：

```sh
sh .codex/governance/governance-hook.sh doctor
sh .codex/governance/governance-hook.sh status
```

主要命令：

| 命令 | 用途 |
| --- | --- |
| `init` | 创建唯一治理状态，不覆盖现有状态 |
| `update-execution-snapshot` | 更新主 Agent 的描述性执行快照 |
| `record-evidence` | 校验并登记可复用证据 |
| `record-repair` | 将真实失败、针对性修复和新证据绑定为修复代次 |
| `request-advisory-review` | 以稳定 `request-id` 请求一次宏观复核 |
| `request-completion-review` | 为完成候选创建复核 |
| `accept-review` | 校验并原样保存 Governor 反馈，不改变执行状态 |
| `record-review-response` | 保存主 Agent 的采纳、拒绝或确认及下一验证点 |
| `resolve-intervention` | 保存真实用户事项的安全解决摘要并创建复核 |
| `prepare-handoff` / `bind-handoff` / `cancel-handoff` | 转移或取消生命周期复核归属 |
| `complete-execution-snapshot` | 在证据检查点成立后关闭当前执行快照 |
| `finish` | 机械核对完成证据并结束治理状态 |
| `doctor` | 在隔离目录完成确定性自检 |

结构化输入默认从标准输入读取，也可使用 `--file <path>` 或 `--json-base64 <base64>`。Governor 通过原生父子通道返回一个 JSON 后，主 Agent 用 `accept-review` 持久化，再用 `record-review-response` 记录自己的判断；不需要中间通知文件，也不存在“应用 Governor 决定”的步骤。`apply-review` 仅为已加载旧指令保留，行为等价于不改变执行状态的明确确认。

任务迁移采用两阶段一次性交接：源任务执行 `prepare-handoff`，在新任务身份已知后执行 `bind-handoff`，再由新任务消费交接标识。准备、绑定、取消和消费都不会冻结源任务或目标任务。

## 本地状态与构建产物

- `.codex/governance/runtime/` 是不提交的本地治理状态，任务进行中不得随意删除。
- 日常持久化只有 `state.json`、`review-request.json` 和 `review.json` 三个固定当前态文件：状态和请求更新时覆盖旧内容，新请求使旧反馈失效，反馈校验通过后覆盖 `review.json`。
- 运行时不创建历史 Review 目录或追加事件日志，也不增加轮转、摘要、归档或第二套记忆；恢复所需事实直接存在于 `state.json` 和有效证据，长期重要结论仍进入项目关键工作留痕体系。
- `.codex/governance/bin/` 是不提交的本地编译产物。
- 配置、Schema、CLI 源码、Governor 和 Hooks 随项目维护。
- 项目级 Hook 定义变化后，Codex 会要求用户重新审阅并信任新哈希；不得通过绕过 Hook 信任来替代正常审阅。

参考：

- https://learn.chatgpt.com/docs/hooks
- https://learn.chatgpt.com/docs/agent-configuration/subagents
