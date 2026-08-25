# LongMemEval‑S 社区基准迁移

## 目标

把第一版唯一社区主基准从现有 LongMemEval‑V2 实现完整迁移为官方清洗 LongMemEval‑S，使同一冻结候选能够按成熟同行口径产生真实、公开、可复核的长期记忆质量、检索时效与成本证据。迁移完成前不得运行 community 正式验收。

## 固定依据

- 产品与验收语义：[`docs/product/requirements.md`](../product/requirements.md)、[`docs/delivery/first-version-delivery-definition.md`](../delivery/first-version-delivery-definition.md)、[`docs/delivery/acceptance-system.md`](../delivery/acceptance-system.md)。
- 选择和比较口径：[`docs/research/first-version-benchmark-basis.md`](../research/first-version-benchmark-basis.md)。
- 官方代码：[`xiaowu0162/LongMemEval`](https://github.com/xiaowu0162/LongMemEval) 提交 `9e0b455f4ef0e2ab8f2e582289761153549043fc`。
- 官方清洗数据：[`xiaowu0162/longmemeval-cleaned`](https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned) 提交 `98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`。

## 必须完成

1. 以 LongMemEval‑S 的时间化用户—助手会话、500 道完整问题和官方评测器重建 community 适配；只经 Ownward 正式创建、组织、检索和读取路径工作，不接触答案、证据标签、题型捷径或评测函数。
2. 在同一候选与等价条件下冻结数据、Reader、裁判、提示、检索预算、计时边界和成本口径；官方问答准确率与 Ownward 自测的检索时延、返回上下文量、资源和外部调用成本分开报告。
3. 同步 Suite contract、adapter、配置示例、绑定、预检、执行、证据、失效关系、汇总、公开说明和测试；保留 `longmemeval` 这一稳定执行层名称，但所有正式入口只能绑定 LongMemEval‑S。
4. 先证明新的配置、绑定、适配与报告可在隔离最小夹具中完整闭环，再删除 `benchmarks/longmemeval_v2/`、V2 专用配置、测试、CI 引用和所有非历史活动引用；不得保留能够误启动 V2 的第二入口。
5. 旧 community 结果全部失效；仍与候选和输入绑定一致的 core、frontier、qualification 与 full 结果继续复用，不得因社区基准迁移整套重跑内部证据。
6. 以最小代表样本实测单位成本，冻结正式全量的墙钟、调用量、资源和费用边界；这里只验证执行路径和成本推算，不运行完整 LongMemEval‑S 正式验收。

## 不得发生

- 不运行 LongMemEval‑M、LongMemEval‑V2 或其他新增社区基准。
- 不修改产品定位、产品代码、架构职责、Ownward 专项数据、真值、评分规则或内核前沿材料。
- 不为了适配基准加入测试专用产品路径、样本规则、答案泄漏或不同于发布默认能力的模型服务。
- 不把不同 Reader、裁判、检索预算、数据版本或计时口径的公开成绩直接排列为同条件前沿。

## 完成条件

- 当前权威文档、Suite 机器契约、正式适配、示例配置、测试和 CI 全部只指向固定的官方清洗 LongMemEval‑S。
- `python benchmarks/acceptance/suite/run.py check`、Suite 全部单元测试、`self-check` 和隔离 community 预检通过。
- 最小样本证明创建、组织、检索、回答、官方评测、报告、检查点、恢复和局部失效链路真实可执行，且成本边界已经冻结。
- 除本任务对已移除对象的必要指认和只增不改的历史记录外，仓库不存在 LongMemEval‑V2 活动引用、可执行入口、无效数据或临时残留。
- 未运行完整 LongMemEval‑S，也未使任何产品功能或有效内部证据退化。
