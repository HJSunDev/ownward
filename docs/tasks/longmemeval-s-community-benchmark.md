# LongMemEval‑S 社区基准迁移

状态：LongMemEval‑S 迁移、Production Profile 执行链、诊断、恢复和正式运行前成本校准均已完成；并发 8 的完整代表预检满足稳定性与 20,400 秒硬上限，已具备正式 500 题 community 运行条件。

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
6. 先对官方 500 题执行不调用模型的确定性 dry-plan，再以四类完整代表题实测单位成本，冻结正式全量的墙钟、调用量、资源和费用边界。每个最多二十项的 Ownward 自然工作批独立分析，批内正文只传输一次；不得跨批合并。必须保持每项工作载荷、候选元数据、模型、提示要求、Schema 和原序校验提交不变，只有真实越界才允许在自然批内原序拆分。这里只验证执行路径和成本推算，不运行完整 LongMemEval‑S 正式验收。

## 不得发生

- 不运行 LongMemEval‑M、LongMemEval‑V2 或其他新增社区基准。
- 不修改产品定位、产品代码、架构职责、Ownward 专项数据、真值、评分规则或内核前沿材料。
- 不为了适配基准加入测试专用产品路径、样本规则、答案泄漏或不同于发布默认能力的模型服务。
- 不把不同 Reader、裁判、检索预算、数据版本或计时口径的公开成绩直接排列为同条件前沿。

## 完成条件

- 当前权威文档、Suite 机器契约、正式适配、示例配置、测试和 CI 全部只指向固定的官方清洗 LongMemEval‑S。
- `python benchmarks/acceptance/suite/run.py check`、Suite 全部单元测试、`self-check` 和隔离 community 预检通过。
- 最小样本证明创建、组织、检索、回答、官方评测、报告、检查点、恢复和局部失效链路真实可执行，且包含安全余量的成本上界不超过正式硬上限。
- 除本任务对已移除对象的必要指认和只增不改的历史记录外，仓库不存在 LongMemEval‑V2 活动引用、可执行入口、无效数据或临时残留。
- 未运行完整 LongMemEval‑S，也未使任何产品功能或有效内部证据退化。

## 当前执行口径与校准结果

- 机器权威为 [`benchmarks/longmemeval_s/protocol.json`](../../benchmarks/longmemeval_s/protocol.json)：500 题、23,867 个会话、1,498 个最多 20 项的 Ownward 自然语义工作批次，最多四个并发问题、八个独立单 turn Codex App Server worker 和 20,400 秒总墙钟硬上限。自然批不得跨批合并；只有超过 Luna 输入或输出安全边界时才允许在批内确定性拆分。
- 正式生产评测口径固定为 Codex `gpt-5.6-luna` / `low` 完成语义组织、Codex `gpt-5.6-luna` / `xhigh` 作为 Reader、Codex `gpt-5.6-terra` / `medium` 按官方评测提示和标签语义执行裁判。Reader 身份来自结果前冻结的可靠性选择：medium 与 high 在完整 oracle 证据上仍有机械失败，xhigh 30/30 通过；Stage 6 与正式评测只使用这一共同身份。三者复用现有 Codex 能力；隔离预检也真实调用三者，但子集结果不形成正式成绩。检索最多返回 24 条资产线索；短资产完整读取，长资产通过公开证据检索/读取取得源绑定区间，每个来源至多三条且合计不超过 8 条证据、Reader 上下文不超过 24,000 字符。
- 正式结果标识为 `Ownward LongMemEval-S Production Profile`。不得把不同 Reader、裁判、题目子集或检索预算下的公开成绩设为通过线或直接排名；完整报告必须封存当前评测口径与原始证据。
- 每题使用独立产品数据目录，逐阶段持久化创建与语义组织检查点；正式报告分别记录官方准确率、Ownward search/read 时延、上下文量、模型 token、语义批次和总墙钟。
- 已复用的 500 题无模型 dry-plan 证明 23,867 个会话与 1,498 个自然工作批完整，摘要为 `8dc1f4c0…b8d56`。历史池 1/2/4 校准继续作为审计证据，但活动选择规则已替换为：在独立单 turn 池 8/12 中选择稳定且完整余量上界不超过 20,400 秒的最低并发。
- 并发 8 的历史四类代表预检共 187 个会话、12 个自然语义工作批、20 次 Codex 调用；20/20 首次完成，0 重试、0 限流、0 中断、0 transport 超时、0 worker 重启，12 批原序提交，20 个独立只读新 thread，报告与检查点恢复后逐字节不变，墙钟 132.359 秒。该结果绑定旧 Luna/medium Reader，只保留传输、批次和历史成本基线价值，不能冒充当前 xhigh community preflight；路径为 `E:\Ownward\acceptance\longmemeval-s\runs\preflight\77600820-89e959e3ae7c92f1-production-profile`。
- xhigh 成本迁移收据 `8ec2dc56…5e96` 把 8.447 秒 Reader p95 对 500 次请求、四并发全部视为新增开销，并只扣除 Stage 4 已封存且含重复误差的 V2 本地关键路径节省；修正后的原始投影为 12,339.621 秒，加入 20% 正常波动、10% 有界重试与 3,600 秒恢复余量后为 19,635.850 秒，低于 20,400 秒硬上限并保留 764.150 秒余量。该收据只证明新 Reader 具有成本可行性，不替代当前候选的 community 重绑与正式四题 preflight；正式 500 题仍未解锁。
