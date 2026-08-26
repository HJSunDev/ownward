# 第一版验收基线依据

第一版采用 [Ownward Acceptance Suite v1](../../benchmarks/acceptance/suite/README.md)：固定内核基线证明资产保存、更新、备份恢复、派生重建、身份稳定、派生状态可销毁和模型可替换；固定 Ownward 专项数据集验证跨时间、多步关联、场景适用、信息更新及关系组织增益；固定版本的官方清洗 LongMemEval‑S 提供社区可比较的长期记忆质量、检索时效与成本证据。三层证据绑定同一候选且分别通过，不能互相替代。

LongMemEval‑S 是第一版唯一的社区主基准。它以约 115K tokens 的时间化用户—助手历史和完整 500 题验证信息提取、跨会话推理、时间推理、知识更新和拒答，直接对应 Ownward 长期个人信息的主体能力；Zep、Mem0、Supermemory 等同类记忆基础设施也公开采用这一口径，因此能够形成真实同行比较。LongMemEval‑M 只扩大同一任务的历史规模，LongMemEval‑V2 则改为多模态 Web Agent 环境经验，两者都不能替代 LongMemEval‑S 的产品适配性与行业可比性，也不为增加测试数量进入第一版。

正式基准固定官方仓库提交 `9e0b455f4ef0e2ab8f2e582289761153549043fc`、官方清洗数据集提交 `98d7416c24c778c2fee6e6f3006e7a073259d48f` 及其中的 `longmemeval_s_cleaned.json`。Ownward 生产侧的语义组织与 Reader 固定使用 Codex `gpt-5.6-luna`，推理强度分别为 `low` 与 `medium`；裁判固定使用 Codex `gpt-5.6-terra` / `medium`。评测完整沿用官方 500 题、问答协议、评测提示和标签语义，Ownward 不接触答案、证据标签或评测函数。正式结果标识为 `Ownward LongMemEval-S Production Profile`，完整记录模型配置、检索预算、计时边界和原始证据；问答准确率与检索时延、返回上下文量及资源成本分开报告，只有完整评测口径等价的结果才可直接比较。

内核前沿优化环使用公开、版本化的固定材料与冻结向量、语义输入，提供不超过三分钟的定向反馈和不超过十分钟的完整反馈。质量、时延和资源独立设防；候选只有在无受保护能力退化且至少一项预先定义的能力实质改善后才能进入固定资格集，资格验证通过后才成为有效内核基线。当前没有能够以同一输入、协议和环境复现的外部同类内核对照，因此内部结果不能被包装成外部前沿。

资源前沿中，信息内核与完整交付仍分别观察，模型成本不能稀释内核判断，内核成本也不能隐藏用户实际承担的完整交付成本。`total-agent-memory v12.4.0` 的既有同机数据只保留为资源研究证据：由于能力合同、输入和执行协议不完全等价，不能换算成 Ownward 内核前沿或正式完成结论。

依据：

- [LongMemEval 官方仓库](https://github.com/xiaowu0162/LongMemEval)
- [LongMemEval 官方清洗数据集](https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned)
- [Zep LongMemEval‑S 说明](https://blog.getzep.com/gpt-4-1-and-o4-mini-is-openai-overselling-long-context/)
- [Mem0 记忆评测说明](https://docs.mem0.ai/core-concepts/memory-evaluation)
- [Supermemory LongMemEval‑S 报告](https://supermemory.ai/research/longmembench/)
- [Ownward Acceptance Suite v1](../../benchmarks/acceptance/suite/README.md)
- [total-agent-memory](https://github.com/vbcherepanov/total-agent-memory)
