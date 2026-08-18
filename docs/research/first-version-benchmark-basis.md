# 第一版验收基线依据

第一版采用两层验收：固定产品数据验证 Ownward 特有的信息范围、语义关系、增量更新和场景适用性；公开基准验证长期记忆的质量—时效前沿。当前固定定义位于 `benchmarks/acceptance/v4/`，不随实现结果调整；此前基线的错误及修正原因永久保留。

公开质量与时效以 LongMemEval-V2 Small 的官方评测器和 LAFS 前沿为主。它直接评估经验、流程、环境陷阱和前提适用性，并同时计入回答准确率与在线记忆查询时延。冻结时公开的 AgentRunbook-C V2 高质量点为 75.61% / 130.54 秒，Ownward 必须在相同读者、裁判、数据和预算下进入或超过该前沿；外部智能体主动检索固定使用官方条件中的 Codex CLI 0.117.0、`gpt-5.4-mini` 与 `xhigh` 推理强度。

资源前沿以 `total-agent-memory v12.4.0` 官方源码标签为同机复现对象：它具备本地持久化、关系组织、384 维混合检索和 MCP 接入，并公开提供长期记忆质量结果。双方在十万条信息、十万组 384 维向量和近十万条关系下比较运行闭包、CPU、内存、存储、持久写入、基础可检索与语义检索内核；可替换模型运行时不计入任何一方。

LoCoMo 与 LongMemEval 继续作为个人长期信息和多跳检索的回归轨道；MRAgent 的公开结果是冻结时“结构化关联 + 累计证据主动检索”的直接基线。不同论文的模型、裁判和统计口径不混合比较，所有对标必须使用各自官方数据与评测器复现。

固定数据是为本次验收编写的合成信息，不包含用户的真实个人信息，也不向体系提供类型或关系答案。金标只供验收器计算准确率、覆盖率、错误合并与拆分、场景泄漏及关系对检索的增益；规模数据由固定种子扩展到一千、一万和十万条，核心语义样本保持不变。

依据：

- [LongMemEval-V2 官方仓库](https://github.com/xiaowu0162/LongMemEval-V2)
- [AgentRunbook-C V2 研究更新](https://xiaowu0162.github.io/longmemeval-v2/agentrunbook-c-v2/)
- [MRAgent 论文](https://arxiv.org/abs/2606.06036)
- [LeanMem 论文](https://arxiv.org/abs/2608.03463)
- [total-agent-memory](https://github.com/vbcherepanov/total-agent-memory)
