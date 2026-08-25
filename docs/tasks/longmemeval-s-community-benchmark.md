# LongMemEval‑S 社区基准迁移

状态：迁移实现、真实三批次校准、隔离预检、恢复和局部失效验证均已完成；尚未提交当前工作区，也未运行正式 500 题 community。提交并按机器绑定规则新增 community scope 后，方可进入正式验收。

## 目标

把第一版唯一社区主基准从现有 LongMemEval‑V2 实现完整迁移为官方清洗 LongMemEval‑S，使同一冻结候选能够按成熟同行口径产生真实、公开、可复核的长期记忆质量、检索时效与成本证据。迁移完成前不得运行 community 正式验收。

## 固定依据

- 产品与验收语义：[`docs/product/requirements.md`](../product/requirements.md)、[`docs/delivery/first-version-delivery-definition.md`](../delivery/first-version-delivery-definition.md)、[`docs/delivery/acceptance-system.md`](../delivery/acceptance-system.md)。
- 选择和比较口径：[`docs/research/first-version-benchmark-basis.md`](../research/first-version-benchmark-basis.md)。
- 官方代码：[`xiaowu0162/LongMemEval`](https://github.com/xiaowu0162/LongMemEval) 提交 `9e0b455f4ef0e2ab8f2e582289761153549043fc`。
- 官方清洗数据：[`xiaowu0162/longmemeval-cleaned`](https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned) 提交 `98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`。

## 持久运行环境

- 当前机器的正式环境根固定为 `E:\Ownward\acceptance\longmemeval-s`；[`benchmarks/longmemeval_s/environment.py`](../../benchmarks/longmemeval_s/environment.py) 是唯一安装与离线完整性检查入口。
- `assets/v1`、`runtime/v1` 和 `manifests/v1.json` 是可跨候选复用的固定只读环境；`runs/<candidate>/` 才能承载候选绑定、报告、日志和临时产物。正式 community 配置必须引用环境清单并把全部输出放在对应候选运行目录。
- 安装动作显式联网一次；清单存在后的重复安装以及 `check` 均只能离线验证并复用，不得隐式克隆、下载或安装。正式验收前必须通过带最小官方入口夹具的 `check --smoke`。
- Suite 的 community 机器实现已经统一为 LongMemEval‑S；正式 500 题仍须在迁移规定的全部非正式验证通过、当前工具形成干净提交并完成 community 绑定后才能执行。

## 必须完成

1. 以 LongMemEval‑S 的时间化用户—助手会话、500 道完整问题和官方评测器重建 community 适配；只经 Ownward 正式创建、组织、检索和读取路径工作，不接触答案、证据标签、题型捷径或评测函数。
2. 在同一候选与等价条件下冻结数据、Reader、裁判、提示、检索预算、计时边界和成本口径；官方问答准确率与 Ownward 自测的检索时延、返回上下文量、资源和外部调用成本分开报告。
3. 同步 Suite contract、adapter、配置示例、绑定、预检、执行、证据、失效关系、汇总、公开说明和测试；保留 `longmemeval` 这一稳定执行层名称，但所有正式入口只能绑定 LongMemEval‑S。
4. 先证明新的配置、绑定、适配与报告可在隔离最小夹具中完整闭环，再删除 `benchmarks/longmemeval_v2/`、V2 专用配置、测试、CI 引用和所有非历史活动引用；不得保留能够误启动 V2 的第二入口。
5. 旧 community 结果全部失效；仍与候选和输入绑定一致的 core、frontier、qualification 与 full 结果继续复用，不得因社区基准迁移整套重跑内部证据。
6. 以最小代表样本实测单位成本，冻结正式全量的墙钟、调用量、资源和费用边界；提效只能对已经固化且互不依赖的语义工作做有界并发分析，必须保持每项工作载荷、候选、模型、提示模板、Schema、最多二十项的 Ownward 工作/提交批次和原序校验提交不变。真实工作批超过冻结的 Codex 输入容量时只允许按原序切成分析调度单元，不得丢项、改写内容或改变提交边界。这里只验证执行路径和成本推算，不运行完整 LongMemEval‑S 正式验收。

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

## 当前执行口径与校准结果

- 机器权威为 [`benchmarks/longmemeval_s/protocol.json`](../../benchmarks/longmemeval_s/protocol.json)：500 题、23,867 个会话、1,498 个最多 20 项的 Ownward 语义工作批次，最多四个并发问题、全局最多八个 Codex 调用和八小时总墙钟。每个 Codex 语义分析单元最多 300,000 字符；该边界只拆分分析调度，不改变工作、候选、提示模板、Schema 或原批次提交。
- 语义组织能力固定为 Codex `gpt-5.4-mini` / `low`，Reader 固定为 Codex `gpt-5.4` / `medium`；两者只复用现有 Codex 认证。官方裁判独立固定为 `gpt-4o-2024-08-06` / temperature 0 / 10 tokens；隔离预检使用明确的确定性裁判夹具，不形成正式成绩。检索最多返回 24 条线索、完整读取 8 条证据、Reader 上下文最多 24,000 字符。
- 官方完整准确率最低线为 82.2%，来源是同清洗数据身份、同 Reader、同官方裁判和最多八条答案证据的公开可复核结果；不同 Reader、裁判或题目子集的更高公开数字只作观察，不直接替换通过线。
- 每题使用独立产品数据目录，逐阶段持久化创建与语义组织检查点；正式报告分别记录官方准确率、Ownward search/read 时延、上下文量、模型 token、语义批次和总墙钟。
- 最终隔离校准选取 `knowledge-update`、`multi-session`、`single-session-assistant`、`temporal-reasoning` 四种题型各一题，共 187 个真实会话和 12 个完整三批次工作批；31 个语义分析与 4 个 Reader 调用在全局并发 8 下用时 165.141 秒，2 次格式重取均在既有有界策略内恢复，限流和中断均为 0，全部工作完成身份/数量/顺序/Schema 校验并以 12/12 原批次提交。按 1,498 个工作批、500 个 Reader 和官方裁判预留推算，全量为 18,912.927 秒；同载荷按旧每题串行分析为 34,295.760 秒。并发 8 已稳定且将投影降低约 44.9%，故冻结最低校准值 8，不再评估 12。原始证据位于 `E:\Ownward\acceptance\longmemeval-s\runs\preflight\ffaeb2f5-70f19288788f4196`，恢复复核证明全部证据逐字节复用。
