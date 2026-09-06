# Mastra Observational Memory：高分来源与 Ownward 可借鉴的机制

2026-09-06。竞品源码研究及 Ownward 证据组织诊断已完成；未运行竞品，未修改正式内核或 Reader，未续跑 500 题。本文为研究依据，不改变产品职责和架构约束。

## 结论

**最值得借鉴的差异是信息的组织和交付粒度：让智能体取得容易正确理解的事件与条件，而不只是取得相关原文。** Mastra 在入库时组织带时间、角色和状态变化的记忆，回答时集中提供；Ownward 当前受测链路主要选择有来源的原文线索，再由 Reader 多轮检索、拼接并判断。前者减少了回答时重新整理历史的工作，但不能据此断言它能解决我们的全部 8 道失败。

“关键事实已交付”能够排除相应事实的召回遗漏，**不能单独排除信息组织与呈现仍有改进空间**。下一步值得验证这种差异，停止以反复更换协作规则作为主要路径。

## 为什么选它，以及分数究竟是什么

选择 Mastra OM，因为其近 95% 的结果确实使用轻量回答模型，而且记忆实现和评测运行器均公开，可以核对机制。不是认定它是所有项目中绝对最强。筛选时也查到 Exabase M-1 宣称 Gemini 3 Flash 达到 96.4%，但其研究明确保留架构细节，未取得可同等检查的核心源码，因此不作为这次实现对照。[Mastra 研究](https://mastra.ai/research/observational-memory)、[Exabase 技术披露范围](https://exabase.io/research/exabase-achieves-state-of-the-art-on-beam-benchmark)。

| 核查项 | Mastra 公布的配置与结果 |
| --- | --- |
| 数据规模 | LongMemEval-S，500 题 |
| 记忆整理模型 | Gemini 2.5 Flash，负责 Observer／Reflector |
| 回答模型 | GPT-5-mini |
| 判分模型 | GPT-4o，使用按题型区分的 LongMemEval 判分提示 |
| 标题中的成绩 | 94.87%，六类正确率的等权平均 |
| 按我们一直使用的逐题正确率换算 | **468／500＝93.6%，32 题错误** |
| 跨对话题 | **116／133＝87.2%，17 题错误** |

逐题换算来自公开分类计数：75＋116＋53＋30＋67＋127＝468。运行器确实按六类平均计算 `overall_accuracy`。[公开结果](https://mastra.ai/research/observational-memory)、[汇总代码](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/explorations/longmemeval/src/commands/run.ts#L1500)。

因此，先前将“95%”表述为竞品普遍平均水平不准确；我们的“每百题最多错五题”也不能直接等同于它的标题分数。它本身同样存在明显的跨对话损耗。此外，轻量是成本和产品档位，不证明 GPT-5-mini、Gemini Flash 与 Qwen3.8 Flash 在这个任务上能力相同；当前没有同条件对照支持谁一定更聪明。

## 源码中真正不同的工作

为避免用后续版本解释历史成绩，本次读取研究发表当日的仓库快照 `27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0`（2026-02-09）；它不是已被证实的该次跑分精确提交。

| 环节 | Mastra 的实现 | Ownward 当前受测链路 | 对我们有价值的方向 |
| --- | --- | --- | --- |
| 信息整理 | Observer 将原对话转为事件记忆，区分陈述、请求、意图；保留具体角色、数值和状态变化 | 当前语义表示选择原文段落索引，摘要和线索均复制原文；每来源最多 8 条线索，单段最多 384 字符 | 在不丢来源的前提下，让一个事实与理解它所需的对象、角色、条件共同呈现 |
| 时间与更新 | 区分说话时间与所指事件时间，标出状态变化；读取时还能附加相对当前时间的标记 | 日期及澄清存在于来源和片段中，由 Reader 结合多次工具结果理解 | 核实能否把事实、适用时间和对应澄清一起交付，减少孤立数字或孤立日期造成的误用 |
| 回答时的上下文 | 从预制记忆加载观察记录；该配置关闭语义检索，回答路径没有主动检索工具；未整理消息仍可作为上下文补充 | Reader 选择搜索、导航、读证据，在多轮结果中自行整合 | 改善 Reader 主动取得的证据视图，减少重复和无关上下文，同时保留矛盾、来源及未知条件 |

具体依据：[Observer](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/packages/memory/src/processors/observational-memory/observer-agent.ts#L119)、[Reflector](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/packages/memory/src/processors/observational-memory/reflector-agent.ts#L50)、[回答配置](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/explorations/longmemeval/src/config.ts#L1148)、[读取预制记忆](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/explorations/longmemeval/src/commands/run.ts#L899)、[Ownward 语义表示](../../benchmarks/longmemeval_s/semantic_representation.py#L60)、[Ownward 证据返回](../../internal/kernelv2candidate/evidence.go#L43)。

这并非“它有语义整理，我们没有”，而是两者整理的产物与使用方式不同。Mastra 的主回答提示只有简单要求；大量具体指令作用于记忆整理阶段。其 Reflector 会在观察记录超过阈值后重组压缩，**并非每题都经过额外反思**：该评测准备代码设置 30,000 输入 token 触发观察、80,000 观察 token 触发反思，不能把所有成绩归功于 Reflector。[准备参数](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/explorations/longmemeval/src/commands/prepare.ts#L879)。

该流程将成本前移到记忆整理；回答步骤较少不代表包含整理的整轮评测一定更快。它接管主智能体的上下文，我们是可供不同智能体主动使用的信息体系，不能为了模仿分数就改变这个产品边界。

## 不能从它的高分推出什么

- **不能断言它答对了我们的 8 题。** 在本次核查的报告与仓库中，未取得与 94.87% 对应的完整逐题输出、精确运行配置和数据哈希；这是可检查源码的项目成绩，不是已独立复现的结果。
- **不能全部照抄规则。** 其上下文包装有“日期已过的计划，若无反证就认为已完成”和冲突时优先较新观察的指令。这些推断不满足 Ownward 对事实、计划与不确定性的区分，可能制造新错。[上下文包装](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/packages/memory/src/processors/observational-memory/observational-memory.ts#L1491)。
- **不能把仓库中的评测选项当成已使用的证据。** 该快照有内容替换预处理、带标记题目判错后重答取成功结果、修订题目／标答的独立统计等路径。未取得发表成绩的运行材料，无法确认这些路径对成绩的实际影响；既不能默认完全同口径，也不能据此指控其成绩造假。[预处理](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/explorations/longmemeval/src/commands/prepare.ts#L1133)、[重答条件与结果替换](https://github.com/mastra-ai/mastra/blob/27e9a34bdb67c6aa59bd45cbaba619b9bd1f44a0/explorations/longmemeval/src/commands/run.ts#L1130)。

## 对当前 8 题的落点与下一步

值得验证的共同假设是：**事实已到达，但事件身份、角色、适用范围及澄清之间的联系没有以足够清楚、集中的形式交付，Reader 承担了本可减少的重组负担。** 这是待验证的机制假设，不是已证实的新内核缺陷，也不保证仅靠呈现就能消除模型判断错误。

例如电影节题应便于区分两地事件；厨房用品题应完整保留新旧物品的替换过程；筹款题应把参与角色与金额所属范围放在一起；工作坊题应把日期矛盾与后续澄清并列交付。这些是现有现场对应的诊断点，不得变为题号、主题或答案规则。婚礼、烘焙、超市和运动题的真实歧义继续保留，不能为了标答删除额外事实或补造日期。

**建议只做一次有明确对照的验证：固定 Qwen、已验证的协作规则和这批实际取得的原文，只改变证据组织与呈现，覆盖这 8 题。** 整理不得使用标答、答案来源标签或人工挑选“正确事实”；不增加查询，不让整理阶段代答，原文始终可核验。若结果和证据使用都改善，再把有效部分纳入公用的信息交付路径并跑固定 24 题；若无改善，就不继续围绕这个假设扩建功能。诊断结果不充当正式分数。

### 实测结果

上述假设已做三组同证据对照：原文重读判对 2／8，原文分组 4／8，事件摘要加原文 3／8。分组新增判对的两题存在判分波动或原判断缺陷未消除；摘要还将两个电影节的相似经历推成同一事件，制造无依据的冲突。**这两种实现尚未证明稳定有效，暂不接入正式链路。** 实验只检验既有证据的阅读组织，不是 Mastra 入库整理、压缩与完整评测的复现，不能据此否定其整套方法。详见[对照方法、结果及审计证据](information-use-exploration.md#三已验证的方向及效果)。

当前有效协作规则保留；三次实验性规则改写未达到目标，工作区已恢复此前规则，未晋升。本轮没有可晋升的新候选，未追加固定 24 题回归。主测试继续暂停，第 118 题是后续续跑位置。

随后加入第 72 题，完成九题 oracle 来源诊断：保留 S 版日期，只给官方标记的证据会话全文，判对 6／9，原文重读对照 3／9。第 95、114、116 题仍判错；新增判对涉及额外相关经历被排除和既有判分波动，不能直接转成产品过滤规则。该诊断使用特权来源选择，不代表 Mastra 表现，也不能计入正式分数。详见[九题诊断与数据审计](information-use-exploration.md#三已验证的方向及效果)。
