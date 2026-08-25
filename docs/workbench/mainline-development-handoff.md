# Ownward 主线开发交接

> **使用边界**：本文只用于把当前主线背景和仓库状态交接给新的 Codex 任务，不是开发提示词，也不授权执行任何开发、修复、验收或 Git 操作。新任务读完后只需确认已经恢复理解，必须等待用户另行发送主线开启提示词。

## 一、为什么需要交接

当前对话虽然长期在处理 Ownward，但 Codex 任务实际绑定的是 `E:\Dev\longxia\zhixing`。通过绝对路径可以操作 `E:\Dev\ownward`，却无法加载和真实验证 Ownward 的项目级 Hook。

Ownward 已经建立自主开发治理运行时。要让主 Agent、Governor 和跨上下文状态闭环按正式流程生效，后续任务必须绑定 `E:\Dev\ownward`，并由用户在该项目中审阅和信任 `.codex/hooks.json`。本次迁移、精确信任和真实冷启动验证均已完成；Hook 定义变化后仍需重新审阅。

这次迁移只改变 Codex 任务的项目归属，不改变 Ownward 的产品方向、开发阶段或下一项工作。

## 二、治理体系建立的背景

Ownward 的持续开发体系原本已经明确：产品目标和需求回答“要什么”，架构与开发规范约束中间过程，交付定义规定完成边界，其余技术判断由智能体自主完成。

实践中曾发生主 Agent 钻入错误的数据准备路径，连续约九个半小时没有产生有效价值。用户打断后，主 Agent 能够立即判断该路径错误，说明问题不是缺少判断能力，而是执行过程中缺少从全局主动审视当前工作的独立视角。

因此新增自主治理闭环：

- 主 Agent 仍是唯一产品实现者；
- Governor 子 Agent 从主线目标和全局进度审查当前工作是否有价值、是否高效、方案是否最优，并在发现问题时指出根因和更优路径；
- 确定性运行时维护工作包、证据、复核、用户介入和跨上下文恢复；
- 首次启动、恢复和上下文压缩后进行固定复核，关键事件可以触发额外复核；
- 治理的目的不是把工作拆小或增加用户参与，而是让主线保持高度自主，同时及时跳出牛角尖。

用户计划在新任务正常工作一段时间后主动打断，检查这套治理闭环是否真实运行。该检查不改变主线任务，也不要求执行者制造表演性流程。

## 三、当前仓库事实

交接形成时的事实如下，恢复时仍须以仓库最新状态重新核对：

- 项目：`E:\Dev\ownward`
- 分支：`main`
- 迁移前基线提交：`1f714e3`；恢复时必须以最新 `git log` 为准
- 形成本文前 Ownward 工作区干净
- 自主治理运行时已经完成安装并通过 `doctor` 自检
- 已在正确绑定 Ownward 的 Codex 任务中完成 Hook 自动触发、Governor 冷启动和单实例 Ownward MCP 验证

最近与本次交接直接相关的提交：

- `148c3aa feat(governance): establish autonomous development governance runtime`
- `58003fd feat(skill): add business flow problem explanation`
- `1f714e3 docs(acceptance): preserve core baseline follow-up context`

治理运行时的主要入口：

- `.codex/hooks.json`
- `.codex/config.toml`
- `.codex/agents/governor.toml`
- `.codex/governance/README.md`
- `.agents/skills/autonomous-development-governance/SKILL.md`

本次 Codex 环境、故障根因、解决方案和以后冷启动预期保存在：

- `.agents/skills/autonomous-development-governance/references/codex-runtime-integration.md`

## 四、当前主线处于什么阶段

第一版尚未完成，也尚未进入最终验收。验收体系的长期推进顺序由 `docs/delivery/acceptance-system.md` 规定。

当前下一阶段是建立首个有效、可复用的内核基线。阶段目标、范围、进入条件、顺序和完成边界保存在：

- `docs/tasks/first-kernel-baseline.md`

该文件是主线阶段参考，不是独立任务入口，不维护自己的平行状态，也不能替代主线持续开发提示词或自主治理运行时。

当前阶段的核心顺序是：

1. 先解决阻碍内部基线成立的 Acceptance Suite 实现问题；
2. 再让同一份干净、冻结候选通过固定内核基线、完整前沿观察和 Ownward 专项资格集；
3. 三层证据同时有效后，晋升首个有效内核基线；
4. 本阶段不运行完整 Ownward 专项集、官方清洗 LongMemEval‑S 或最终汇总。

## 五、当前尚未解决的问题

当前问题及经过多轮审查形成的解决预期和方案保存在：

- `docs/workbench/problem-resolution-workbench.md`

这个问题仍然成立、尚未实现修复。它不是一条孤立的 LongMemEval 预检错误，而是 Acceptance Suite 没有统一、准确地区分以下关系：

- 某个层级为什么运行；
- 开始执行前需要满足什么资格；
- 实际消费哪些输入、候选制品、工具和环境；
- 哪些变化应该使哪些证据局部失效；
- 哪些证据参与基线晋升。

已知直接表现是：当前内部阶段没有启用 `community`，却仍会在预检中访问社区基准远程仓库。但只删除这一条检查不能解决根因；同源问题还涉及分层预检、候选绑定、入口加载、工具与输入清单、定向检查点身份、证据复用、局部失效和晋升完整性。

工作台已经写明问题边界、最强反方复盘、解决预期和完整解决方案。后续执行者必须读取全文并结合最新代码复核，不能从聊天摘要自行重造方案。

该问题只允许修正 Acceptance Suite 当前内部基线链路及其直接说明，不得改变产品代码、产品架构、验收标准、固定数据、真值、评分规则、完整专项或社区基准自身能力。修复阶段只运行相关单元测试、隔离自检和非正式预检，不提前运行正式证据层。

## 六、已经清理且不得恢复的旧路径

此前为了一个过度拆小的独立任务，在 `E:\Dev\ownward-acceptance` 建立过候选副本、缓存和 `stage-status.json`。这套平行状态与新的自主治理闭环职责重叠，已经清理。

- `E:\Dev\ownward-acceptance` 已删除；
- 旧 `stage-status.json` 不再是有效状态；
- 旧候选和旧外部检查点不得恢复或作为当前证据使用；
- 首个内核基线任务本身仍然需要完成，只是必须由主线和新的治理状态推进。

## 七、新任务恢复时需要知道什么

新任务应先阅读本文并确认理解，但不得因此直接开始开发。用户随后会另行发送 `docs/workbench/development-workbench.md` 中“持续完成第一版开发”的开启提示词。

收到开启提示词后，执行者应按提示词和治理运行时恢复事实，重点读取：

- 产品、架构、工程规范和第一版完成条件；
- `docs/delivery/acceptance-system.md`；
- `docs/tasks/first-kernel-baseline.md`；
- `docs/workbench/problem-resolution-workbench.md`；
- `benchmarks/acceptance/suite/` 的当前实现与契约；
- `docs/records/README.md` 要求的当前记录和直接引用记录。

预期的第一项主线工作是：在治理闭环下复核并彻底解决当前 Acceptance Suite 内部依赖与证据生命周期问题；问题解决后，再进入首个有效内核基线建立阶段。具体工作包仍须由主 Agent 依据最新仓库事实提出，并经 Governor 从全局价值、效率和方案质量角度复核，不能把本文当成跳过恢复与复核的执行命令。
