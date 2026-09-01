# V2 内核大版本迭代

状态：当前 subject `a0a59771…117` 的 Stage 4 与干净 HEAD `a74a2e1…65e9` 封存的 Stage 5 制品继续有效；frontier、core、qualification、full 四层同身份恢复均为零执行复用。旧 15 题 `3d418feb…ad3` 的原始 `evaluation-process-error` 保持不可改写，独立裁决 `4c34f5dc…e3a1` 只确认其非超时单尾在同依赖 60 请求 p95 `533.533<=553 ms` 下属于评测环境离群，候选结论仍未知。通用答案归因边界已机械区分候选证据稳定不足与外部 Reader 首答随机性，且不会污染事实交付、时间/冲突或性能。由有效 5 题 `b07facf0…04c7` 重新生成的 15 题 `4f65af5b…32d` / `dfeeae6e…f064` 已真实通过；随后全新 25 题 `c204269d…fe91` / `fb212b78…9ef` 因独立成立的时间正确率 `0.8<1.0` 被候选门拒绝，事实交付完整、冲突正确率 1.0、检索 p95 `500<=553 ms`，答案归因流程的不稳定没有洗白该独立失败。V0 与 50 题未运行。Stage 3 定向裁决 `c64ccb97…91d7` 已用与盲测不重合的 9 个时间更新、先后、适用性案例复核 60 个独立来源和固定 8 次读取竞争：21/21 必要事实均被搜索返回并读取，9/9 最终答案及时间正确性通过，未出现组织、排序、适用性或交付偏离。因此 25 题失败继续有效，但没有被洗白为已证内核根因；候选源码与 Stage 3—5 直接依赖均未改变。本轮停在协调者复核点，不生成新关卡、不修改内核。

## 职责

本文是 V2 大版本的唯一执行与调度状态，负责把[内核持续演进体系](../engineering/kernel-evolution-system.md)落实为可连续派发、复核、恢复和结束的任务。方法、产品、架构和验收定义仍由引用文档负责，本文不建立第二套规则。

固定依据：

- [V1 最终测试质量偏差分析](../incidents/kernel-v1-final-benchmark-quality-gap.md)
- [V0 版本档案](../engineering/kernel-versions/v0.md)与[V1 版本档案](../engineering/kernel-versions/v1.md)
- [产品需求](../product/requirements.md)、[整体架构](../architecture/overview.md)与[验收体系](../delivery/acceptance-system.md)
- [持续协调项目推进](../workbench/development-workbench.md#持续协调项目推进)

## 目标

以 V0 为唯一对照，完成一次真实泛化的内核大版本迭代：候选在信息组织质量、检索与最终回答质量、端到端效率上形成整体大幅跃升，V0 已有正确能力无实质退化，并保持或提高经同尺重证且与失败机制可分离的 V1 收益；通过固定回归、内部整体验证和 5、15、25、50 题一次性盲测后，最终达到可运行正式 LongMemEval-S 的标准。

本任务结束不等于 V2 晋升。正式 LongMemEval-S 及其晋升判断不在本任务范围内。

## 当前基线

| 对象 | 当前事实 | 本任务用法 |
| --- | --- | --- |
| V0 评测基线 | 内核世代 `1952f4b8…7b77`、v6 映射后的内核效果身份 `61729fb8…017a`；版本档案、原二进制和历史证据已冻结，是唯一有效比较基线 | 以现有制品和等价映射建立当前边界下的同尺对照，不修改或重算其历史正式结果 |
| 当前产品 | 组合 `c068ae20…751e`、内核 `db18a3a1…2684`，迁移后保持 V1 行为 | 只作为当前运行实现；不得冒充有效基线或 V2 候选 |
| Acceptance 状态 | 唯一 binding/state 当前为 v6/v3；它是可变运行态校准输入，不是永久比较政策。`baseline=null` 不取消版本化 V0 评测基线 | 阶段 1 至 4 只读；显式校准只封存当前 state、binding、报告和历史摘要且证明 state 字节不变。阶段 5 合法重绑产生新的运行态校准身份，不改变比较政策、V0/V1 制品或不依赖该校准的既有非正式证据 |
| V1 失败候选 | `285/500`；101 题改善、58 题退化；检索平均时延由 `41.2 ms` 增至 `347.8 ms` | 不是第二基线；正式结果、合成视图和局部通过只作诊断。其收益必须在同尺条件下重新证明，并与失败机制可分离后才能进入 V2 保护边界 |
| 迭代能力 | `benchmarks/acceptance/suite/kernel_iteration_run.py` 是唯一通用非正式入口；比较、执行、阶段 3 与阶段 4 合同分别冻结共享政策、执行职责、材料及当前问题链门槛。原子计划/检查点可恢复且不能写正式 state；V1 专用入口已退出生产路径 | 阶段 4 通过同一入口选择独立 V2 subject，按直接依赖只运行受影响开发/回归证据；候选制品不替换当前组合 |

## 范围与约束

- 只修改信息组织与检索内核及其不可缺少的迭代、验证和恢复能力；不新增产品功能，不改变产品、架构或正式验收合同。
- V0 评测基线、当前产品实现和 V2 候选是三个不同身份，各自拥有独立制品、状态和证据，可分别选择测试。V2 必须作为独立内核世代准备、验证和淘汰，不得原地改写当前组合或污染权威资产、当前内核和有效证据；产品只保持一个当前内核，只有后续受控晋升才能切换。
- V1 的实现思想只能在独立 V2 候选内按证据复用、替换或舍弃；经同尺重新证明且与失败机制可分离的 V1 收益必须由 V2 保持或进一步提高，但不因此把 V1 设为第二基线或保留其具体实现。当前产品的 V1 等价世代在本任务内保持可用且不被原地修改，旧世代清理只能发生在后续正式晋升与观察成立之后。
- V0 与 V2 是彼此独立的内核身份；比较时只冻结相同的评测数据、工具、模型、提示词、评分器、配置和环境，将内核版本及其专属制品与直接依赖作为唯一预定变量。Git 提交不能替代组件、组合与执行身份。
- 不读取、复制、改写或针对正式 LongMemEval-S 题目、答案、Gold ID 和正文进行开发；正式结果只允许抽象为通用缺陷。
- 开发集与回归集必须依据抽象能力和根因独立构造，不得从 V0/V1 正式题目中筛选、转写或保留可还原内容；盲测生成、质量准入和候选评价彼此隔离。
- 信息组织质量、检索与最终回答质量、端到端效率分别冻结和判定；有提升空间的维度必须各自达到大幅跃升门槛，高基线维度必须保持，不得用一项收益抵消另一项退化。局部指标、资产命中或固定开发集满分不得关闭方向或大版本。
- 已足够优秀或不限制整体结果的方向只建立回归保护，不强行修改；任何优化都必须反绑真实瓶颈。
- 日常反馈保持分钟级，只重跑真实失效部分；正式 500 题、重复环境准备和无新证据的高成本验证禁止进入本任务。
- 本任务产生的开发、回归和盲测证据属于 Acceptance Suite 管理的非正式候选证据，不写正式 `state.json`、不改 V0 历史结果、不晋升任何内核。一次性盲测只能进入非正式迭代入口，不得加入正式 `contract.json` 的执行模式或证据层；正式验收禁止冻结后动态生成正式证据的边界保持不变。
- 执行者不得执行 Git 暂存、提交、历史改写或推送；提交与下一工作包由协调者依照持续调度规则处理。

## 阶段状态

| 阶段 | 状态 | 唯一交付 | 关闭条件 |
| --- | --- | --- | --- |
| 1. 比较合同与恢复基线 | 已完成 | V2 迭代合同、V0 同尺对照清单 | 受版本控制的冻结基线、V0 小型聚合事实、V0/V1 世代目录和当前组合在干净检出中机械闭合；三维 V0 基线、大幅跃升/不可退化门槛、重复误差、共享条件、唯一变量、失效图和分钟级成本上限均在无 V2 结果时冻结。v3 合同只纠正经独立审计证明错误的检索时延身份，质量维度与旧证据兼容身份保持不变；当前 v6/v3 只经显式只读校准进入非正式证据，合法阶段 5 重绑不改变政策身份；正式 state 未写入 |
| 2. 验证能力补齐 | 已完成（困难 oracle Reader 已独立重新资格化） | 版本化验证入口与非候选校准报告 | validation `059ea0ab…ccb6` 的生成与准入资格继续有效。实际 15 题 `e0c18bb…b680` 证明 Terra/high 在完整 oracle 上机械不稳定后，新合同 `89fff13d…c155` 只把失败后诊断 Reader 冻结为 Terra/xhigh，产品首答 Luna/xhigh、提示、renderer、Schema 与 Judge 均不变；五题非候选困难资格 `53760d7f…2ae4` / `8841a424…17c9` 完成 25 次 Reader、40 次 Judge，产品/oracle 零失败、Judge 正误对照各 5/5，65 次模型调用、零候选与产品执行，恢复零执行。Stage 3—5、当前 5 题及历史 15 题候选观察均未被重写 |
| 3. 端到端归因与材料冻结 | 已重新关闭（当前候选） | 问题池、开发/回归清单、五方向决策 | 不读取或反推已销毁盲测内容；旧 subject 在独立两题上的产品上下文 6/6 失败、完整来源 6/6 正确，机械定位到每来源 evidence 深度三项之后仍有预算内必要事实未交付。当前 subject 的同一诊断与事实不重合确认均为产品/完整来源 6/6，Judge 接受 24/24 Reader 输出且正误对照通过；观察器逐字重放、同身份恢复和正式 state 只读成立 |
| 4. V2 候选迭代 | 已重新关闭（当前候选） | 独立 V2 内核世代及净提升证据 | 依赖迁移 `fd82621e…998` 只保留未受 source-context 变化影响的历史成本/表示证据；当前开发 `e29d2b3f…d6a` 4/4、回归 `22a606b2…cde` 8/8，事实交付、时延和恢复保护重新绑定 subject `a0a59771…117`。Stage 4 门槛、模型、向量空间和正式 state 未改写 |
| 5. 内部整体验证与冻结 | 已完成（当前候选） | 冻结候选清单与完整内部报告 | 干净候选冻结 `e43e5666…599`；frontier/core/qualification/full 在同一 binding 下全部通过，full 24/24，四层同身份恢复均返回 `reused` 且 state 与场景证据树逐字不变 |
| 6. 一次性盲测 | 全新 15 题通过；25 题候选失败后停止 | 不含原始题面的四级累计报告 | 旧 15 题原结果与两次后续评测流程失败均只作不可改写历史。通用答案归因边界成立后，全新 15 题 `4f65af5b…32d` / `dfeeae6e…f064` 候选 15/15、V0 14/15、事实交付/时间/冲突全过，检索 p95 `453 ms`，同身份恢复零执行。全新 25 题 `c204269d…fe91` / `fb212b78…9ef` 的事实交付完整、检索 p95 `500 ms`、冲突正确率 1.0，但时间正确率 `0.8<1.0` 独立失败，候选被不可逆拒绝；V0 与 50 题未运行，原始内容已销毁 |
| 7. 最终测试交接 | 被 Stage 6 真实候选失败阻塞 | 最终测试就绪报告 | Stage 6 尚未完成且当前候选已由 25 题时间正确性绝对门拒绝；不得运行交接预检、正式 LongMemEval-S 或晋升 V2 |

阶段按直接依赖推进和失效：V2 比较合同或共享评测条件变化，只重开阶段 1 的受影响部分及依赖它的 V2 比较证据，不使 V0/V1 内核或其既有证据失效；验证实现、数据质量准入或校准失败只重开阶段 2 的受影响部分，不计为候选失败；开发、回归或整体验证失败回到阶段 3 至 5；盲测候选失败销毁当轮数据，只保留通用根因并回到阶段 3，以不重合案例重新迭代，重新冻结后用全新数据从 5 题级重启。任何失败都不得无条件重做仍有效的前置工作。

## 五个方向

V1 结果只提供初始观察，不预先决定根因或强制修改方向。

| 方向               | V1 已知观察                                                                  | 初始状态 |
| ------------------ | ---------------------------------------------------------------------------- | -------- |
| 信息表示与组织结构 | 首候选用有界双向来源连续窗口与最大边际覆盖选择把长多事实交付从 2/5 提升为 5/5；开发 4/4、固定回归 8/8 | 决策：优化；当前问题链已关闭，方向不因单链通过而提前关闭 |
| 检索架构与算法     | 有界来源广度调度消除多来源加载缺口；持久连接、惰性证据探测和安全请求内复用已消除可证固定开销。四 worker 系统线程预算收敛后，完整消费者 p95 为 `356.673 ms`，在同责 `600 ms` 门及冻结误差外保留余量 | 决策：优化；多来源加载与整体时延均已关闭 |
| 语义能力与表示模型 | 长资产答案来源召回在当前产品与 V0 上均为 `1.0`；召回成立且与后续片段缺失可分离 | 决策：保护 |
| 数据结构与存储架构 | 旧候选每资产同时保留 pending 与 ready 派生记录；新 subject 在全部语义工作完成后无损压实为每资产唯一最新耐久记录，并于 ready 耐久+索引完成后释放重复运行态原文和向量；最终同尺产品数据比率为 `0.401517` | 决策：优化；当前产品数据字节与运行态重复问题链已关闭，语义 Token 与墙钟不得用该收益补偿 |
| 执行架构与状态维护 | 最终表示生命周期把当前 revision 的原文向量作为有界、可失效执行任务与外部语义分析重叠；并发更新释放旧 revision 等待者，只有派生记录耐久写入和语义索引更新后才确认完成并删除任务表项。耐久失败保留单份可重试结果，embedding 失败可重建，队列压力不将已成功权威写入伪装为产品失败；最终证据由相同身份恢复逐字复用，零模型与零产品执行 | 决策：优化；当前端到端墙钟问题链已关闭 |

每个方向分别维护“决策：优化 / 保护”和“状态：待审视 / 进行中 / 已关闭”。优化方向以当前冻结候选的端到端提升和回归证据关闭；保护方向以没有真实瓶颈、既有能力无实质退化的证据关闭，不能把“保护”本身当作完成状态。

阶段 3 机器终态为 `.tmp/kernel-v2-major-iteration/stage3/869633da…478d/result.json`，证据身份 `79051919…3543`。四组同尺执行总墙钟 `227.734` 秒，低于 `420` 秒目标和 `600` 秒硬上限；当前产品开发为 3/4，唯一错误由逐项证据证明为片段缺失，当前产品与 V0 固定回归均为 8/8。V1 收益裁决为：长资产语义召回、批量耐久进入保护；细粒度证据与已证根因不可分离而拒绝保护；存储收敛未达到减半门槛而拒绝保护。该证据只作非候选诊断，`candidate_decision=null`，没有建立 V2 候选。

阶段 4 当前问题链合同为 `iteration/v2/stage4-contract.json`（身份 `43625b14…0c90`）；门槛在任何候选质量结果前冻结，当前路线在前序路线被拒后、接触本路线结果前冻结。前序仅补前文的路线由开发证据 `672e0a5b…6b63` 证明仍为 2/5，已作为失败路线保留；当前独立 subject `784fb1bf…be69` 绑定内核世代 `7893f0b3…28c4`、内核效果 `557921aa…d5e1` 和自包含候选制品。机器终态 `.tmp/kernel-v2-major-iteration/stage4/784fb1bf…be69/result.json`（身份 `3412a348…4d1b`）证明开发 4/4、长多事实 5/5、回归 8/8、长资产语义召回 1.0，开发/回归检索 p95 为 `297/203 ms`，受影响反馈墙钟 `105.031` 秒；两份结果重复恢复均逐字一致且零模型、零产品执行，正式 state 摘要前后保持 `3c7826e0…fcdc`。该证据只关闭首问题链，`stage4_complete=false`，未运行盲测、阶段 5 或正式验收。

多来源问题链材料与门槛由 `iteration/v2/stage4-multisource-contract.json`（身份 `f2df8554…1d1d`）冻结。首候选的逐阶段轨迹证明：搜索已返回全部目标来源，但旧对角顺序会让前三个读取名额重复进入前位来源深度，导致排名 5—7 的必要来源在八个名额内未加载；这只证明首个偏离点，不把相关性冒充更深根因。实现路线 `00a88fef…9b0` 冻结为有界来源广度优先、单一深来源保持原深度；未改善墙钟的并行探测 subject `b3db693d…b1bda` 作为失败路线保留。最终 subject `7b78a0ef…4fbd` 绑定内核世代 `a55dc5b2…a2c` 与内核效果 `ca652a67…562`，机器终态 `.tmp/kernel-v2-major-iteration/stage4-multisource/7b78a0ef…4fbd/result.json`（身份 `fed22ad7…d8c`）证明新材料 3/3、六项必要事实 6/6、全部必要来源均返回并读取；原开发 4/4、长多事实 5/5、固定回归 8/8、长资产语义召回 1.0。最大读取数仍为 8、最大上下文 `4754/24000` 字符、持久状态增长为 0，受影响执行墙钟 `158.375` 秒。一次性原始 p95 受共享查询长尾影响为多来源/开发 `375/375 ms`；预先封存、零模型零写入的平衡成对复核分别得到候选相对首候选 `+16/-31 ms`，均在冻结的 `47 ms` 重复误差内，回归 p95 为 `219 ms`。三份结果以相同身份重复恢复均逐字一致且零模型、零产品执行，正式 state 仍为 `3c7826e0…fcdc`。该证据只关闭多来源加载问题链，`stage4_complete=false`。

检索时延问题链继续使用独立只读同尺材料和既有 prepared data。旧性能 `04c9cc0c…bc68` 混用了传输身份，只保留诊断；完整语料组织排序即使在 8/10/24 资产材料上触发，也要求全部资产落入请求宽度且逐项具有词面覆盖，而正式 LongMemEval-S 每题通常有 38—62 个会话，真实个人信息库更大，因此生产主路径必然回退精确查询向量。候选 `2a9f04d1…184c6` 的窄场景性能只保留为淘汰诊断，不能关闭时延链；组织排序实现及其候选制品绑定已从当前转换删除。

精确向量运行时校准 `20c20331…13ff` 在同一 EmbeddingGemma 模型、前缀、维度、池化、归一化和截断下比较 `2/4/6` 线程及 `1/2` 有界并行；六组向量逐分量漂移均为 `0`。本机 6 核 12 线程下实测最优配置为 `6` 线程、`parallel=2`，峰值工作集约 `202.9 MB`，单请求 `59.05/79.54 ms` mean/p95、两请求竞争 p95 `137.56 ms`。它优于其余受测配置但精确查询推理自身已经超过 `78.001 ms` p95，未给固定证据交付留下 `41.201/78.001 ms` 端到端门槛余量，因此只冻结为下一候选的共享运行时配置，当前产品运行源码与身份保持不变；该收益不能计作 V2 内核独有收益，也不能关闭 `retrieval-latency`。下一路线必须继续保持精确查询向量与全部质量职责，机械分离剩余推理、调度和真实 38—62 资产端到端成本。

真实规模同尺结果 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/real-scale-v1/result.json`（身份 `72b6988e…33f4`）在同一 `6/2` 运行时、同一模型/空间、四个正式 worker、38/46/54/62 资产、24 搜索/8 读取/24000 字符下完成三代平衡测量。候选检索 mean/p95 为 `419.640/596.211 ms`，其中 Search 为 `360.254/514.614 ms`、证据搜索 `29.326/51.862 ms`、读取 `30.059/58.805 ms`；四 worker 精确查询向量 p95 为 `378.845 ms`，而隔离精确查询 p95 为 `112.850 ms`。全部目标来源均返回并读取，选择轨迹稳定，向量逐分量漂移为 `0`，prepared data 与正式 state 前后逐字不变；该证据把首个放大定位为精确查询推理及四个独立运行时的 CPU 竞争，而非已收敛的证据交付。`6/2` 只通过候选 overlay 进入三代非正式比较；当前产品源码、组合身份与正式制品没有变化。

结果前冻结的后续调度合同 `a60a11d…af66` 及机器结果 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/vector-runtime-followup-v1.json`（身份 `94e543b7…42f9`）继续比较隔离 `6/2`、2/3/4 个有界活动 worker 与四查询批处理；所有向量逐分量漂移仍为 `0`。隔离 `6/2` 已为 `85.655/99.618 ms` mean/p95，2/3/4 worker p95 分别为 `259.471/307.783/284.414 ms`，批处理每查询吞吐成本 `95.940 ms`。因此当前固定模型与运行时的精确单请求下界自身已经高于完整检索 `41.201 ms` mean 门，调槽、增加 worker 或批处理均无数学余量关闭该门；`retrieval-latency` 保持进行中，不运行因性能先决条件失败而失效的模型质量重测。唯一下一验证点是在模型、空间和精确向量不变的前提下，先找到并机械证明隔离精确查询推理低于完整门槛的运行实现；在该下界成立前不得把共享运行时或候选证据优化记为问题链关闭。

固定模型运行实现评估 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/runtime-implementation-assessment-v1.json`（身份 `0656ac12…6662`）以相同 EmbeddingGemma Q8_0 GGUF、空间、前缀、池化、归一化、截断和 `1e-7` 漂移门核对官方 llama.cpp `b10488` 可交付制品。CPU 为 `85.655/99.618 ms`；Vulkan 为 `216.240/242.892 ms` 且最大分量漂移 `0.003215`；OpenVINO 在首个热身查询返回 HTTP 500；SYCL 在本机没有可见设备；CUDA 12.4/13 与当前驱动 `442.62` 所暴露的 CUDA 10.2 不兼容。没有实现通过结果前冻结的 `<41.201 ms` mean、`<=62.4 ms` p95 与 `1e-7` 漂移三道隔离门，因此均未进入质量或真实规模集成，下载探针未成为产品依赖。`retrieval-latency` 继续打开；本问题链的唯一下一验证点收敛为冻结一个更高效的语义表示模型，并在任何采用前重新证明现有全部质量保护，本工作包不实施该下一路线。

更高效语义表示筛选合同 `iteration/v2/stage4-semantic-model-screening-contract.json` 在结果前把候选限定为仓库既有研究中的 Multilingual E5 Small 与 Multilingual MiniLM L12 v2，并冻结各自官方修订、许可、前缀、池化、归一化、截断、新向量空间、执行顺序及早停门。组件 mean/p95 门 `28.0/49.9 ms` 由完整检索 `41.201/78.001 ms` 减去同尺 V0 证据读取与协议下界后冻结；广度质量复用 512 条中英、跨语、长短、近似干扰冻结集和独立 24 条时间/冲突补测，不使用正式 LongMemEval-S 或阶段 4 候选材料。机器结果 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/semantic-model-screening-v1.json`（身份 `f1b6d5c6…b972`）显示：E5 Small 为 `33.325/42.628 ms`，mean 无余量，按早停合同未执行质量；MiniLM 为 `14.760/20.126 ms`，但整体/跨语 Recall@10 仅 `0.6875/0.15625`，时间/冲突补测亦明显低于参照，故在建立向量世代前淘汰。两者的固定修订、MIT/Apache-2.0 许可和 ONNX CPU 制品均已机械封存，筛选前后正式 state 保持 `3c7826e0…fcdc`；没有胜者、没有新 V2 向量世代或端到端候选。`retrieval-latency` 继续打开，唯一下一验证点收敛为具备精确失败开放的分层检索/语义回退架构；本包不实施该路线。

精确失败开放分层路线在实现候选前冻结合同 `iteration/v2/stage4-hierarchical-retrieval-feasibility-contract.json`，要求跳过语义后返回 ID、顺序、前四关系种子、来源覆盖、读取集合和上下文字节对所有合法语义结果均不变；缺证、异常或合同漂移一律保留原 EmbeddingGemma 精确路径。等权 RRF 的机械最坏界为：词法第 2 名加语义第 1 名 `0.03252247`，高于词法第 1 名加语义第 3 名 `0.03226646`，因此在至少三个语义候选的多结果请求中，未观察语义通道足以改变顺序和下游交付。只读可行性结果 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/hierarchical-retrieval-feasibility-v1.json`（身份 `0aaee5f0…5f8d`）复核 38—62 资产真实规模、同源长事实、时间冲突、多来源、V0 正确能力、独立干扰与语义改写共 22 个既有完整语义 oracle 请求：安全证书为 `0/22`，低于结果前由 mean 成本与 p95 最多 5% fallback 共同冻结的 `95%` 覆盖门。路线在候选实现、模型调用和产品执行前淘汰，正式 state 仍为 `3c7826e0…fcdc`。首个不可消除事实是：在当前融合与质量合同下，精确语义排序不是可由低成本静态事实证明冗余的可选阶段；同时现有精确推理物理下界高于冻结端到端门。`retrieval-latency` 保持打开，唯一下一验证点收敛为保留 EmbeddingGemma 精确语义质量，机械重审 `41.201/78.001 ms` 是否错误沿用了未承担当前两阶段语义与证据职责的 V0 非同尺边界；本阶段不擅自改门或进入资源成本。

同尺性审计合同 `iteration/v2/stage4-retrieval-latency-comparability-audit-contract.json`（身份 `e2a33fe…3bf8`）在任何新候选测量前冻结入口/终点、包含工作、交付完整性、资产与并发负载、统计口径及外部语义归属。只读审计结果 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/comparability-audit-v1.json`（身份 `f2ef1f64…c8b3`）机械还原：V0 community 的 `41.2/78 ms` 只统计 `ownward_search` 与按前缀贪心的完整会话 `ownward_read`，没有证据规划、`evidence_search/read` 或完整上下文交付职责，且 500 题中有 116 项目标未搜索返回、133 项已返回但未读取；当前 V2 指标则覆盖从来源搜索到有界证据规划、搜索、读取和上下文形成的完整消费者路径。两者的职责、交付完整性、38—62 资产负载和重复统计均不等价，外部语义推理仍在两边分别记录。迁移收据 `55131b81…0cec` 因而把 V0 数字降为历史诊断，只把时延维度迁移到既有 `direction-budget-selection` 的完整消费者 `p95 <= 600 ms` 权威门；`47 ms` 冻结重复误差必须完整预留，候选决策上限为 `553 ms`，不另造 mean 门。比较合同升级为 v3，政策修订身份 `b5400416…198b`，旧 `b1e3b07a…fbbe` 仅作为未受影响证据的兼容身份；质量、同源片段、多来源、恢复和五题预算证据不失效。既有真实规模结果 `72b6988e…33f4` 与新职责、四 worker、24/8/24000、38/46/54/62 资产和三轮平衡重复机械同尺，无需补测；其完整交付 p95 `596.211 ms` 加重复误差为 `643.211 ms`，仍超过 `600 ms`，所以 `retrieval-latency` 不关闭。此前围绕 `41.201/78.001 ms` 的运行时、轻量模型与分层跳过拒绝结果只保留机制诊断，不再承担候选时延淘汰或关闭。

尾部优化合同 `iteration/v2/stage4-retrieval-latency-tail-contract.json`（身份 `4d3e435a…1903`）在探针结果前冻结观测 `p95 <= 553 ms`、逐字质量等价、资源/恢复边界，以及 `43.211 ms` 缺口外再保留 `20 ms` 工程余量，要求隔离路线至少提供 `63.211 ms`。机器结果 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/tail-probe-v1.json`（身份 `dfeba514…e1cb`）用既有 prepared data 做同请求分解：决定性 p95 请求的 Search 为 `719.914 ms`，证据搜索/读取为 `24.502/22.118 ms`；即使按两阶段理想并行，下界也只把 p95 降低 `36.775 ms`，不足冻结余量。一个 `6/2` 精确服务承载四并发的 p95 为 `441.876 ms`，反而比四个独立服务的 `378.845 ms` 慢 `63.030 ms`，并产生 `0.002115` 最大分量漂移，越过 `1e-7` 合同。两条授权路线都在候选实现、模型调用和产品变更前淘汰，正式 state 与 prepared data 前后逐字不变；首个剩余事实仍是四 worker 下精确查询 Search 的 CPU 竞争，但本工作包禁止第三条猜测路线，`retrieval-latency` 保持打开。

系统级线程预算合同 `iteration/v2/stage4-retrieval-latency-system-budget-contract.json`（身份 `0555bdfe…6564`）在结果前冻结产品原生 2/1、四独立 worker、38/46/54/62 资产、24 搜索、8 读取、24000 字符及三轮平衡重复。候选 subject `208982be…517f` 删除错误的 6/2 runtime overlay，仍使用同一 EmbeddingGemma、向量空间和精确查询；既有 `20c20331…13ff` 校准证明最大向量分量漂移为 `0`。机器性能 `d4f859b8…0436` 的 36 个样本得到完整消费者 mean/p95 `295.549/356.673 ms`，比 `596.211 ms` 改善 `239.538 ms`，同时通过 `553 ms` 判定门与 `533 ms` 工程余量门，因此未运行 3/1。终态证据 `.tmp/kernel-v2-major-iteration/stage4-retrieval-latency/system-budget-final-v1/result.json`（身份 `10b84de7…4caa`）证明开发 4/4、长多事实 5/5、多来源 3/3 与 6/6、固定回归 8/8、长资产语义召回 1.0；最大读取 8、最大上下文 4754、持久状态增长 0。源 prepared data 和候选隔离数据前后逐字不变，同身份恢复逐字幂等且零模型、零产品执行，正式 state 仍为 `3c7826e0…fcdc`。`retrieval-latency` 由“四进程合计 24 推理线程/8 槽位超过 12 逻辑核预算”这一根因关闭；阶段 4 的唯一下一验证点是 `end-to-end-resource-cost`。

端到端资源合同 `iteration/v2/stage4-end-to-end-resource-cost-contract.json`（身份 `7fce2ebc…9bc0`）在补测结果前冻结同一开发/回归材料、Luna/Luna/Terra、工具、提示与评分、候选先于 V0 的执行顺序，以及 semantic 输入、端到端墙钟和真实产品数据字节三个彼此不可补偿的 `<=0.5` 比率门；资产、控制、派生/索引和测试隔离成本分别计量，禁止靠目录迁移或重分类降低指标。成对证据 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/paired-v1/result.json`（身份 `fdae96be…ca73`）得到 V0/V2：semantic input `57466/57163`（比率 `0.994727`）、墙钟 `112.515/103.532 s`（`0.920162`）、Ownward 产品数据 `503152/330232 B`（`0.656327`）；测试、日志、报告和非正式检查点另列为 `1028925/1064977 B`，没有计入产品字节。两边均为 12 道相同独立材料、12 次 semantic、12 次 Reader 和 12 次 Judge，结果与检查点同身份恢复逐字复用且零模型、零产品执行，正式 state 前后仍为 `3c7826e0…fcdc`。

旧首根因诊断 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/root-diagnosis-v1/diagnosis.json`（身份 `bd7a720f…0895`）只证明两代各有 46 个工作项、46 个唯一正文、12 次分析调用且正文各传一次；它没有拆分 Codex Token，也没有恢复并发关键路径，现降为历史诊断。结果前冻结的可控性审计合同 `iteration/v2/stage4-resource-cost-controllability-audit-contract.json`（身份 `84d30456…87bb`）禁止用字符比例猜 Token、用阶段累计冒充墙钟或按 V2 结果倒推门槛。只读审计 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/controllability-audit-v1/audit.json`（身份 `ea214c62…719e`）闭合 V0/V2 的 12 份原始 Codex 收据与并发关键路径；由于旧收据没有分类 Token，它只保留全局数为失败开放的诊断，不据此迁移门槛或授权协议。后续原生匹配差分补齐了该证据缺口，本文不再把“缺分类计数”列为当前下一验证点。

同一审计逐帧校验 12 份 v4 派生日志，证明 `295243 B` 中有 `126148 B` 是已被最新 ready 记录取代的 pending 记录；使用现有无损 `Compact` 语义的只读反事实为 `204084/503152=0.405611`。因此首个已证且候选可控的根因转为“语义静默后未压实过期派生记录”。新独立 subject `5279d3ca…f63cb`、内核世代 `d3a918fb…f7d6` 只在全部语义工作完成时压实；存在待处理工作时保持原日志，失败返回且可由同一 submission 幂等恢复。机器终态 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/storage-final-v1/storage-result.json`（身份 `f66a03ba…ea18`）在相同材料和模型职责下得到产品数据 `204913 B`、V0 比率 `0.407259`，所有派生日志过期字节为 0；开发 4/4、长多事实 5/5、固定回归 8/8、事实交付完整、长资产语义召回 1.0，最大读取 8、最大上下文 2928 字符，受影响检索 p95 `188 ms`，12/12 semantic、Reader、Judge 调用未减少。同身份恢复逐字一致且零模型、零产品执行，正式 state 仍为 `3c7826e0…fcdc`。semantic Token `57373/57466=0.998382`、墙钟 `100.047/112.515=0.889188` 仍未通过且不得由存储收益补偿；当时的下一验证点是补齐精确 Token 分类，现已由下述原生匹配差分完成。

原生匹配差分合同 `iteration/v2/stage4-resource-cost-matched-calibration-contract.json`（身份 `72fbe01f…2677`）在新请求前冻结现有 24 份完整收据、相同 Luna/low、逐请求原 Schema、新只读 thread、禁用工具、8 个独立单 turn App Server、四路活动上限和 cache=0。机器结果 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/matched-calibration-v1/calibration.json`（身份 `7fc4d2eb…1162`）以每个 Schema 的最小请求和通用语义指令各运行一次，共 48 个最终成功请求；一次 cache 不一致按合同失败关闭，只删除该请求终态并有界重试，其他 47 项逐字复用，最终零限流、零 worker 重启、正式 state 不变。V0 Token 被机械闭合为固定宿主/Schema `36971`、通用语义指令 `2148`、工作 payload `18347`，总计 `57466`；固定项本身已超过原全局减半目标 `28733`。V0 的 Reader/Judge、宿主边界和冻结语义输出工作同样使全局墙钟减半不可达。迁移收据 `iteration/v2/stage4-resource-cost-component-gate-migration.json` 与终态决策 `iteration/v2/stage4-resource-cost-final-decision.json`（身份 `1c64dde4…7d9d`）保留全局数字为历史诊断，不修改正式 Acceptance，只把不变的“至少减半”规则绑定到 V0 真实候选组件：semantic 指令加 payload 上限 `10247.5` Token；产品数据比仍须 `<=0.5`；墙钟边界由下述可控性收据继续收敛。

紧凑协议可行性合同 `iteration/v2/stage4-resource-cost-compact-feasibility-contract.json`（身份 `fcd3c920…d539`）在结果前冻结无损 indexed body/context 表：46 个 work、46 份唯一正文、稳定身份、revision、上下文、候选 similarity/关系、工作顺序、模型和输出 Schema 均可机械原样恢复，不跨问题或用户合批。单臂结果 `93c5b999…6585` 的跨时窗墙钟只保留为诊断，不能决定路线；纠正后的同窗 AB/BA 合同 `iteration/v2/stage4-resource-cost-compact-balanced-contract.json`（身份 `2ef1d281…2562`）用同一 App Server 池、AB/BA 顺序和 24 对同请求冻结重复误差。机器终态 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/compact-balanced-v1/balanced-result.json`（身份 `3e5e7236…215b`）得到 compact-current 配对均值 `+0.731462 s`，低于同请求重复误差 `5.306917 s`，并确认 Token 组件 `9674<=10247.5`，因此授权实现。

候选专属清单 `manifests/kernel-candidates/v2/resource-cost/semantic-representation.json`（身份 `fe1b9777…7442`）只允许非正式 V2 subject 使用；正式 `protocol.json`、V0、V1 与当前产品不变。当前 subject `a6b545a4…d86a`、内核世代 `5299e5a5…3734`、组合 `48a113a8…ed0` 把表示作为真实语义能力组件和直接依赖封存，通用执行器只消费组合声明，不存在候选专用开关、包装器或 monkeypatch。终态审计合同 `iteration/v2/stage4-resource-cost-real-capability-wall-contract.json`（身份 `40f44ae0…a252`）和结果 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/real-capability-wall-audit-v1/result.json`（身份 `c7480729…a1b`）机械复算 12 份冻结请求的 prompt/Schema 逐字等价，真实轨迹为 12 次分析、46 个工作项且事实无损；开发/回归结果 `3c66aa50…f653` / `d845b416…95fb` 为 4/4、8/8，长多事实 5/5，同身份恢复逐字复用且零模型、零产品执行。Token 与数据字节维度现已关闭。

已被后续对称门替代的历史迁移曾冻结三次同二进制、同 13 资产/3 问题的创建样本 `14.968/15.203/14.203 s`，合并重复误差界为 `1.000 s`。`CreateBatch` 子阶段合同 `iteration/v2/stage4-resource-cost-create-probe-contract.json`（身份 `6a56da47…c067`）与零模型结果 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/create-probe-concurrent-v1/result.json`（身份 `b034f0bc…ba5`）闭合每轮 `14.337592 s` 创建 envelope：文档 embedding 为 `14.317216 s`，其中 `ensureRunning` 为 `9.089263 s`；authority、derived 与内存索引合计不足 `0.02 s`。可控性迁移收据 `iteration/v2/stage4-resource-cost-local-wall-component-migration.json`（身份 `325409de…a1a2`）中的 `7.748142 s` 门与 `11.580875 s` 当前值只保留为历史诊断，不能再作为活动判定。

旧门下结果前冻结的文档批处理合同 `iteration/v2/stage4-resource-cost-batch-feasibility-contract.json`（身份 `8395c3a3…c067`）只允许每项不超过 `320` 字节、每请求不超过 `32` 项，长项保持单独请求，并以两轮 AB/BA 验证。机器结果 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/document-batch-feasibility-v1/result.json`（身份 `e00d32ff…e6e`）把每轮 embedding 调用由 `8` 降至 `3`，12 个输入的顺序与逐项向量身份完全相同；但两轮 envelope 配对收益仅 `+0.388470/-1.359153 s`，保守收益为负。该路线已淘汰，没有进入候选，也没有触发模型或质量重跑；它只保留为历史拒绝证据。正式 state 始终为 `3c7826e0…fcdc`。

对称 CreateBatch 合同 `iteration/v2/stage4-resource-cost-matched-create-contract.json`（身份 `e385a475…6d83`）在结果前冻结同一 13 资产/3 问题、产品原生 2/1 运行时、两轮 AB/BA 和 1 秒重复误差；V0/V2 计时观察器分别与冻结二进制完成创建结果差分，V0 与 V2 的 12 个共同短正文及精确向量逐项一致。机器结果 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/matched-create-v3/result.json`（身份 `30375b31…e554`）得到 V0/V2 CreateBatch envelope `12.485917/11.310522 s`、embedding `12.445194/11.290982 s`、ensure-running `6.338075/6.972264 s`；V2 的权威写入、派生持久化与创建总量都没有相对 V0 回归。因 V0 还嵌入长正文而 V2 延迟到语义结果，二者完整推理输入不相同，合同失败关闭地只对称扣除共同 ensure-running 下界，未把推理或调用调度伪装成共享成本。由此唯一活动墙钟门的 V0 可控基线为 `13.423813 s`、半数门为 `6.711907 s`，当前 V2 为 `9.508403 s`、加误差为 `10.508403 s`，仍差 `3.796497 s`。

结果前冻结的非 CreateBatch 分解合同 `iteration/v2/stage4-resource-cost-non-create-decomposition-contract.json`（身份 `872961f0…c3ca`）只读复用了相同原始时间线、Codex 收据与 CreateBatch 对称结果；机器终态 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/non-create-decomposition-v1/result.json`（身份 `1f949ae4…7520`）闭合同一次关键路径：`15.846478 = 14.968000` CreateBatch `+ 0.613478` semantic 提交/产品执行 `+ 0.265000` 检索/证据。此前 `4.535956 s` 是把另一轮 `11.310522 s` CreateBatch 均值从 `15.846478 s` 中相减所得，其中 `3.657478 s` 是跨观测差值，不是可优化关键路径。真实同次非 CreateBatch 上界仅 `0.878478 s`，即使全部删除也比所需 `3.796497 s` 少 `2.918019 s`；因此没有数学余量、没有授权路线、没有新模型或产品执行，墙钟维度与阶段 4 保持打开。

原始正文向量生命周期合同 `iteration/v2/stage4-resource-cost-raw-vector-lifecycle-contract.json`（身份 `125278c6…abb7`）在实现和新测量前冻结写入、pending、语义工作、提交、ready 查询、重启/重建和失败恢复边界。只读机器证据 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/raw-vector-lifecycle-v1/result.json`（身份 `553029ae…42d6`）证明：短正文向量在创建时参与语义候选并封存 `WorkReference`；成功提交后它不会被正式语义分析向量替换，仍是 ready 检索表示；启动和重建也优先复用同空间当前修订向量。共同启动成本后的原始正文推理包络为 `4.952907 s`，毛余量超过 `3.796497 s` 缺口，但要保持语义工作候选逐字等价就必须在 `semantic_work` 返回前完成同样 12 次推理，完整创建—语义—检索关键路径净可移除上下界均为 `0`。省略、后台化或改用语义分析向量分别会改变候选、引入未封存债务或改变 ready 排序与恢复身份，因此本路线在候选构建、模型和产品执行前淘汰；正式 state 仍为 `3c7826e0…fcdc`。首个真实架构根因收敛为派生记录单一、无来源类型的 embedding 字段同时承载 pending 原文候选/查询与 ready 语义检索职责；下一验证必须先独立冻结并证明 pending—ready 表示世代及其候选、查询、恢复等价，不能继续把它当作单纯调度成本。

表示生命周期合同 `iteration/v2/stage4-resource-cost-representation-lifecycle-contract.json`（身份 `e39272da…93ce8`）纠正了上一审计的适用边界：内部 candidate/WorkReference 不是外部产品字节合同，但任何变化必须产生新 subject，并重证事实交付、答案、时间冲突、查询、耐久与恢复。只读可行性 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/representation-lifecycle-feasibility-v1/result.json`（身份 `fa20e1bf…fd38`）证明 17 个独立案例的 35 项真值全部存在于本工作自身当前权威正文；46 项语义工作的外部分析窗口足以覆盖 `4.952907 s` 原文向量任务，预测加误差为 `5.655496 s`，具有通过 `6.711907 s` 门的数学余量，才授权实现。

首版 subject `f632bed3…a3aef` 证明 pending→ready 能够移出同步创建关键路径，但独立复核发现它在 ready 派生记录已耐久并索引后仍把每个资产的完整原文和向量留在 `jobs`，资产规模会线性放大运行内存，因而该 subject 只保留为定位证据，不是当前终态。

最终 subject `834e204c…f9f17c`、内核世代 `66a6e827…fa2db`、内核效果 `a2ad626f…ef922` 在不改变向量和索引语义的前提下建立精确 revision 完成释放：创建先耐久写入 pending，语义提交与 pending 查询只 join 精确 revision；只有向量已耐久写入派生记录并完成语义索引更新后才删除活动 job，已取得 job 引用的并发等待者读取副本后才清空载荷。未耐久成功不调用完成释放并复用单份结果；embedding 失败的 job 退出活动表并允许后续重试；新 revision 会使旧 job/等待者返回 stale，长资产更新也显式失效旧任务。机械测试证明 256 个 ready 资产后活动 job 数仍为 0，80 资产队列压力下权威写入全部成功并在队列释放后恢复 pending 表示。开发/回归证据 `c1609a82…cbb3` / `afc337b7…9215` 为 4/4、8/8，事实交付完整，长多事实 5/5；semantic input 合计 `47627`，产品数据 `202024 B`、V0 比率 `0.401517`，检索 p95 `141 ms`。

结果前冻结的 AB/BA 合同 `iteration/v2/stage4-resource-cost-representation-final-contract.json`（身份 `db0de5be…a927`）把 12.7 秒相同外部语义分析窗口排除，仅计创建、语义工作、提交和 ready 查询，并绑定最终候选源码、转换、制品、质量结果与正式 state。机器终态 `.tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/representation-lifecycle-final-v6/result.json`（身份 `3e06bfd3…fff72`）的两轮候选关键路径为 `0.466160/0.485877 s`，保守值加 `1 s` 误差为 `1.485877 s`，低于 `6.711907 s`；V0/V2 三题的资产、工作、ready 与查询行为身份一致。候选准备恢复现在同时校验源转换、表示生命周期与全部直接依赖；质量与终态同身份恢复均逐字复用且零模型、零产品执行。

该终态 Acceptance Suite 单元测试 `275/275`、LongMemEval-S 适配器测试 `41/41`、全量 Go test/build/vet、候选标签集成测试、`check` 与 `self-check` 均通过；正式 state SHA-256 前后保持 `3c7826e0…fcdc`。阶段 4 已关闭，唯一下一验证点是阶段 5 内部整体验证与冻结；本阶段没有运行盲测、正式 LongMemEval-S 或任何正式验收。

阶段 5 合同 `benchmarks/acceptance/suite/iteration/v2/stage5-internal-validation-contract.json`（身份 `e7a27b75…fa1e`）与组件清单 `manifests/kernel-candidates/v2/stage5-components.json`（身份 `d7df5e20…0bec`）在任何新内部结果前冻结。干净 HEAD `572d514…083c` 重建制品的包装 subject 变化为 `bb71c834…`，但内核世代 `66a6e827…fa2db`、内核效果 `a2ad626f…ef922`、组合 `6768d65d…d291`、语义/向量身份和全部直接依赖与 Stage 4 subject `834e204c…f9f17c` 逐项一致；二进制 SHA-256 为 `18148338…fbcb`，正式 release 与 100k 生产规模制品亦绑定同一 runtime。冻结结果 `.tmp/kernel-v2-major-iteration/stage5/freeze.json` 身份为 `8a08485a…c839`，最小启动 preflight SHA-256 为 `84708c12…2418`，均在 Acceptance 执行前成立。

Acceptance 正常生命周期把唯一活动验收对象重绑到 V2，而没有切换当前产品。Stage 5 工作区 `.tmp/kernel-v2-major-iteration/stage5/acceptance-v2` 的 frontier/core/qualification/full 报告 SHA-256 分别为 `f04af6b1…697a`、`4d4af8db…2881`、`dcc0eebe…37e1`、`976a1eb8…04b6`：frontier 的审计 candidate 为干净源码提交，绑定组件中的内核效果为 `a2ad626f…ef922`；其余三层 candidate 均为 runtime `66a6e827…fa2db`。full 24/24 全部通过，四类各 6 题，召回、精确率、NDCG、事实回答与 grounding 均为 1.0；Ownward 查询最大 `309.557 ms`，资源峰值 `252.372 MiB`。qualification 首轮在执行器 `1603 s` 有界窗口安全停止，随后仅用 4 个 sealed result、3 个 agent checkpoint 和 1 个 progress checkpoint 补齐，续跑 `116.711 s`；full 复用这 8 项后补齐 24 项，记录墙钟 `3469.797 s`。仅 s02 因首次回答未绑定已读证据触发一次合同内重试，第二次接受；没有失败工具调用、限流、worker 重启或身份降级。

终态 frontier/core/qualification/full 的 `--resume` 分别在 `2.037/2.405/8.138/7.173 s` 返回 `reused`，正式 state SHA-256 前后均为 `0587d7f9…da01`，证明零模型、零产品执行的精确复用。三条 V0 基线历史身份保持不变，V0/V1 制品与旧报告字节未被重写；当前 state 的四个活动检查点只指向 V2。执行器修复只纠正真实验收边界：空 MCP resource/template 发现作为无产品内容的协议元数据审计，非空发现仍拒绝；只读查询在冻结快照前可完成已接受的可重建派生世代，冻结后的任何字节变化仍拒绝。Acceptance Suite `277/277`、LongMemEval-S 适配器 `41/41`、`check`、`self-check` 与差异检查通过。阶段 5 因此关闭，唯一下一验证点为阶段 6 的全新 5 题一次性盲测；尚未生成或运行任何候选盲测，也未运行正式 LongMemEval-S。

提交 `990f6d7…2b` 的 source-context 修复只改变 candidate-only access、消费者执行器及包装来源；语义请求、受控成本、存储、内核效果、模型和向量空间未变。依赖迁移收据 `9ac6f78d…7646` 将失效限定为当前开发/回归、access、二进制、release、组合与 product 消费证据；当前开发 `a8246708…61f7` 为 4/4、长多事实 5/5，当前回归 `bbce8a0f…e668` 为 8/8，两者事实交付完整且同身份恢复零模型、零产品执行。干净发布二进制 SHA-256 为 `66adc0f4…c0fe`，runtime 为 `aa4be7b8…77ea`，内核效果仍为 `a2ad626f…f922`；Stage 5 冻结身份为 `a792cab8…721`。

正常 rebind 精确失效旧 frontier/core/qualification/full 后，在 `.tmp/kernel-v2-major-iteration/stage45-current/stage5/acceptance-v2` 重建四层。新报告 SHA-256 依次为 `099ba229…4f30`、`5983a36f…886f`、`fb0422f9…adde`、`dca13e27…b4d`。qualification 首次在 `1555 s` 硬上限安全停止：4 个结果已封存、3 个场景已有 agent 结果、仅 s05 尚缺终态；同一 `--resume` 只补该真实缺口并在 `65.871 s` 记录 8/8。full 复用这 8 项后补齐其余 16 项，`3516.314 s` 完成 24/24；四类各 6 题的 recall、precision、NDCG、事实回答与 grounding 均为 1.0，Ownward 查询最大 `518.949 ms`，资源峰值 `251.375 MiB`。唯一 state 当前 SHA-256 为 `1d15b267…42eb`，四个检查点均绑定当前制品，三条 V0 基线历史保持不变；只剩不属于本候选内部验收的 `longmemeval` 无效记录。Acceptance 全量单元运行先通过 336/337，唯一失败是独立候选身份夹具硬编码旧 runtime；夹具改为读取封存清单后，对应身份模块 11/11、受影响生命周期 6/6、LongMemEval-S 适配器 41/41 通过，`check`、`self-check` 与差异检查通过。该轮没有生成或运行 Stage 6 关卡。

当前 Stage 6 只更新 5/15 题合同到冻结 subject、runtime、内核效果与 Stage 5 合同，身份分别为 `12dbdbb5…6dc`、`7e244c71…e0a1`；25/50 合同和历史证据没有改写。首次 5 题启动前，评价器资格因把整个 LongMemEval 执行器当作直接依赖而对仅检索交付变化失败关闭；职责投影现只绑定资格实际消费的 `session_content` 与 `_answer_prompt`，一次性迁移 `e98a1c84…a3a9` 机械保留原资格结果 `885192c9…1284`，候选/产品执行为 0，没有重跑资格。

全新 5 题计划 `074aa3b…7631` / 结果 `37e5bd58…6b77` 首批准入 5/5，V2 与后续同材料 V0 均为 5/5，事实交付、时间与冲突全部正确；V2/V0 完整消费者 p95 为 `516/485 ms`，V2 semantic input `21492<=27893` Token、产品数据 `135326<=360910 B`，候选判定墙钟 `371.379<=406 s`。生成、准入、V2 与 V0 均零重试、零限流、零中断、零 worker 重启；恢复逐字一致且零模型、零产品执行。

以该 5 题为前级的全新 15 题计划 `e0c18bb…b680` / 结果 `da47b42a…debb` 首批准入后完成候选执行：事实交付 15/15，首答 14/15，首个观察缺口为 `evidence_read_answer_incorrect`，V0 因候选绝对门未通过而未运行。按冻结原序只诊断第一项失败时，产品上下文 2/3、完整 oracle 上下文 1/3 正确且两者均机械不稳定，Judge 正误对照各 1/1 通过；故该终态是 `evaluation-process-error`、`candidate_decision=null`，没有把不可归因的失败记给候选。候选判定墙钟 `570.722<=751 s`，45 次候选调用零重试、零限流、零中断、零 worker 重启；全部原始内容与 scratch 已销毁，plan identity 恢复模型/产品执行均为 0，正式 state 仍为 `1d15b267…42eb`。本轮在首个评测流程失败处停止，没有生成或运行 25/50 题。

上述真实失败使旧 Terra/high 三题容易资格退出当前评测依据。结果前冻结的 v4 合同 `89fff13d…c155` 使用五个不含候选或盲测内容的困难完整-oracle 案例，覆盖时间权威链、四跳多会话、表格脚注、权威高于新近性和多事实充分性，并把失败后诊断 Reader 提升为 Terra/xhigh；产品首答、Judge、官方提示、Schema 和候选执行均不变。资格计划/结果 `53760d7f…2ae4` / `8841a424…17c9` 完成 25 次 Reader 与 40 次 Judge，产品及 oracle 上下文均零失败，Judge 正误对照各 5/5，零限流、零 worker 重启，墙钟 `101.379 s`，保守投影 `1788.104<1961 s`。scratch 已销毁，正式 state 前后同为 `1d15b267…42eb`；同 plan 恢复零模型、零产品执行。

评测流程资格成立后，仅从有效 5 题前级运行一次全新且不重合的 15 题：计划 `4e151138…e977`、结果 `8b0e8168…7ce1`（文件 SHA-256 `d6dfdfb0…eb82`）。首批 15/15 通过准入；V2 与随后同材料 V0 均为 15/15，事实交付缺失 0，时间与冲突正确率均为 1.0。V2/V0 完整消费者检索 p95 为 `235/375 ms`，semantic input 为 `64776/83840` Token，Ownward 数据为 `405206/1091302 B`；V2/V0 各 45 次 Codex 调用，零重试、零限流、零中断、零 worker 重启。候选判定墙钟 `692.692<=751 s`；恢复证明两侧报告与检查点逐字一致且模型/产品执行均为 0。全部可逆内容与 scratch 已销毁，正式 state 未写。本工作包按边界在 15 题通过后停止，未生成或运行 25/50。

25/50 合同只更新当前冻结 subject、内核世代和 Stage 5 合同，身份为 `6c56ec62…daa9`、`a90d61f8…26f5`，题量、质量、顺序、预算及评测资格均未改变。以有效 15 题为前级的全新 25 题计划 `610b289f…0898` / 结果 `44b2691c…2821` 首批 25/25 通过准入；候选事实交付 25/25、时间与冲突正确率 1.0、检索 p95 `328 ms`，但首答为 23/25，两个首缺口均为 `evidence_read_answer_incorrect`。冻结原序第 9 项的单题归因使用已资格化 Terra/xhigh：产品上下文 3/3 失败、完整 oracle 3/3 正确且机械稳定，Judge 正误对照各 1/1 通过，故可靠归因为 `candidate-context-failure`、`candidate_decision=false`。候选 75 次 Codex 调用零执行重试、限流、中断与 worker 重启；生成 25 次中只有一次合同内结构重试。候选判定墙钟 `893.979<=1097 s`，V0 因候选绝对门失败未运行。原始内容及 scratch 已销毁，同 plan 恢复零模型、零产品执行，正式 state 未写。50 题未生成或运行；本问题链停止并回到 Stage 3 独立不重合复现与优化。

阶段 6 由唯一控制器和统一 CLI 顺序承载 5/15/25/50 四级关卡；当前四级 v3 合同身份为 `12dbdbb5…6dc`、`7e244c71…e0a1`、`6c56ec62…daa9`、`a90d61f8…26f5`。题量、覆盖、预算、100% 首答绝对门和顺序不变；新增的只是 Reader 选择与失败归因直接依赖。首答正确才继续；首答错误且证据缺失是候选失败；证据完整时，候选上下文稳定失败而 oracle 稳定才是候选上下文失败，oracle/逐字相同提示机械失稳或 Judge 对照失败则是评测流程失败并停止后续级别。输出措辞或哈希变化只记录，不作为失败。历史收据保留全部旧诊断，旧关卡均不适用于当前控制器。

历史 5 题计划 `9eeedb71…865`、合同 `2430ff1d…6e0`、终态 `851f80be…a2bb2` 曾形成 5/5 结果；其题面、真值、证据和逐题输出已销毁。该结果现在只证明旧共享生成—准入身份下的历史行为，不再是阶段 6 当前检查点，也不能作为 15 题的前级。

历史 15 题合同 `e9037692…61d`、计划 `dfe88193…e9e7`、终态 `b90d36ce…981d` 曾停在三批准入拒绝 `2/2/1`；候选/V0 调用均为 0，原始内容已销毁。它暴露了旧终态只保存拒绝总数、无法定位生成—准入偏差的缺口，现已降为历史诊断。

阶段 2 可靠性合同 `iteration/v2/stage2-blind-admission-reliability-contract.json`（身份 `fd1b4403…2c6a`）冻结 15 题五类各 3 题、两批资格、单批 `492 s` 和总计 `984 s` 门。旧诊断 `9c2d1af9…522913` / `2e1f794e…edeb95` 与失败关 `0990ee37…f6313` / `743c43f5…9707` 留下的不可逆聚合继续只作历史诊断。当前 validation `059ea0ab…ccb6` 的事实绑定、错误对照、问题线索和全部质量检查未变；生成使用 8 个独立 Terra/xhigh 单-turn worker，有拒绝时只重生被拒题，保留其余题逐字不变，并对替换后的完整冻结顺序重新执行同一 Terra/medium 准入，最多三轮且失败开放。

当前非候选资格计划 `953ec789…ceab6` / 结果 `a3e2ceb0…7990` 的两批相互独立材料均为 15/15，墙钟 `301.5254139/258.2949089 s`，总计 `559.8203228<984 s`；生成/准入调用 `30/2`，第一批有 1 次有界基础设施重试、零限流，候选与 V0 执行均为 0。两批原始内容均已销毁；全新进程仅凭 plan identity 终态复用为零模型、零产品执行。预算收据 `blind-calibration-budget.json`（身份 `07bb9639…aaf`）中的 `406/751/1097/1961 s` 正常预算和 `320/492/665/1097 s` 失败预算未放宽；旧 4 路资格 `dc3c83fc…ddf8` / `4426307c…0f` 原样保留为历史证据。Stage 4/5 候选、合同、门槛、正式状态与既有证据未改变。

当前 validation 上的全新 5 题计划 `d6ae3689…f3f94` / 结果 `af5680c0…a9b4` 在第二批完成独立准入，第一批只留下不可逆拒绝聚合；生成/准入调用 `10/2`，最大生成并发 4 且每 worker 至多一个活动 turn。V2 先通过绝对门后才运行同材料 V0，双方最终回答、事实交付、时间与冲突均为 5/5；V2/V0 semantic input 为 `21585/28052` Token、Ownward 数据为 `134767/360957 B`，V2 完整消费者检索 p95 为 `235 ms`。候选判定墙钟 `316.9942629<406 s`；V2/V0 各 15 次 Codex 调用，零重试、零限流、零中断、零 worker 重启。全部可逆内容和运行 scratch 已销毁；全新进程仅凭 plan identity 返回 `reused`、零模型、零产品执行且直接依赖有效，正式 state 仍为 `0587d7f9…da01`。该结果是当前控制器唯一有效的 5 题前级，下一动作是全新、不重合的 15 题关，本轮未启动。

当前 5 题前级上的全新 15 题计划 `bf9d6e6e…c5b1e` / 结果 `f05272ee…6f5b4` 同样在第二批完成独立准入，第一批只留下不可逆拒绝聚合；生成/准入调用 `30/2`，生成有一次合同内结构重试，零限流和中断。V2 先通过绝对门后才运行同材料 V0，双方最终回答、事实交付、时间与冲突均为 15/15；V2/V0 semantic input 为 `65235/84497` Token、Ownward 数据为 `404329/1093077 B`，V2 完整消费者检索 p95 为 `141 ms`。候选判定墙钟 `727.3009483<751 s`；V2/V0 各 45 次 Codex 调用，零执行重试、零限流、零 worker 重启。全部可逆内容和运行 scratch 已销毁；全新进程仅凭 plan identity 返回 `reused`、零模型、零产品执行且直接依赖有效，正式 state 仍为 `0587d7f9…da01`。该结果继续作为本轮 25 题的有效前级。

该前级上的全新 25 题计划 `c9b4b189…0c5a1` / 结果 `d7071af7…129dc` 在第三批完成独立准入，前两批各有 1 个 `multi-session-relation` 被先验拒绝；生成/准入调用 `75/3`，零重试、限流和中断。V2 绝对门与同材料 V0 相对门均通过：V2/V0 最终回答 `25/25`、`24/25`，事实交付、时间与冲突均完整，V2/V0 semantic input 为 `108837/140274` Token、Ownward 数据为 `673993/1842743 B`，V2 完整消费者检索 p95 `187 ms`。原结果仍保持 `evaluation-process-rejected`、`candidate_decision=true`、`candidate_failed=false`，SHA-256 `c6d1055e…bf294`，未被重写。迁移收据 `3d1bc41e…5fa2` 只把未来材料生成改为 8 路单-turn worker、拒绝题局部替换和整集复审；非候选资格 `953ec789…ceab6` / `a3e2ceb0…7990` 两批 15/15，墙钟 `301.5254139/258.2949089 s`、总计 `559.8203228<984 s`，生成并发均实测 8，候选/V0 执行均为 0，终态按 plan identity 恢复为零模型、零产品执行。正式 state 未变；25 题候选通过现为全新 50 题合法前级。

该前级上的全新 50 题计划 `0f80dc78…f670a` 使用冻结 8 路局部替换调度，在第二轮将五类覆盖各 10 题的完整集合准入为 50/50；`admission.json` SHA-256 为 `6f7d33f4…8380`。终端栈位于 `kernel_iteration_blind_gate.py:391` 的首答失败归因调用；源码控制流机械证明候选 `_execute` 已返回、绝对门已判为未通过且 V0 尚未启动。归因加载官方 `assets/v1/source/src/evaluation/evaluate_qa.py` 时，当前 Python 环境抛出 `ModuleNotFoundError: backoff`，控制器以退出码 1 停止。由于控制器只在归因之后通过 `_finish` 原子写 `result.json`，异常退出时没有耐久候选报告；随后按强制清理边界销毁 scratch，使候选聚合指标、绝对门具体失败列表和最终“候选/评测流程”责任分类无法无损恢复。不可逆收据 `.tmp/kernel-v2-major-iteration/stage6/evidence/blind-gate/0f80dc78…f670a/execution-failure.json`（身份 `6839dd05…fa8e`）如实记录这项证据丢失、未运行 V0、清理和正式 state 摘要；原始材料与运行 scratch 已销毁，无残留 worker。该计划禁止恢复或原样重跑，Stage 6 继续打开。

历史官方评测器环境合同 `0332c8a7…c61f` 曾以独立单题材料资格化 Luna/xhigh Reader、Terra/medium Judge、官方 renderer、产品重复 2/3 与 oracle 重复 1/2/3；计划/结果 `aa5e282f…5c49` / `42d293c6…a42f` 的 5 次 Reader 与 8 次 Judge 均通过。它证明该简单单事实材料上的执行边界，却不能反驳后来 `c45a62f1…5e90` 在完整 oracle 上 3/3 失败的实际能力缺口，现只保留为历史资格证据。控制器仍按冻结材料原序只选择首个实际首答失败题，绝不依据期望答案选择，也不把其他 49 题送入 Reader/Judge；同答案 Judge 检查点仍以 kind/context/repeat/answer 摘要唯一命名。

终态控制器在候选 `_execute` 与绝对门完成后，先原子写入不含题面、答案或证据的 `candidate-observation.json`；归因、Judge、依赖或其后控制器异常均写 `evaluation-process-error` 终态，保留候选聚合、报告/检查点/诊断摘要身份与绝对门，并明确 `candidate_failed=false`、回到 Stage 2 评测能力闭环而非 Stage 3 候选优化。随后才销毁原始材料与 scratch；同身份终态恢复不调用模型或产品。官方环境资格收据成为所有新盲测计划的直接依赖，漂移或缺失会在生成材料、运行候选之前失败关闭。

修正控制器上的全新 50 题计划 `dd4afa9b…f0050` 在第三轮完成 50/50 先验准入；前两轮分别只局部替换 2 个被拒项，最终五类质量检查全部 50/50。候选首次完成 50 题与 150 次 Codex 调用，零重试、零限流、零中断、零 worker 重启；首答 `49/50`、事实交付完整、时间与冲突正确率均为 1.0，完整消费者检索 p95 `188 ms`。候选 observation `d129cf84…7c3e` 在归因前耐久保存；绝对门因首答准确率 `0.98<1.0` 未通过。现有终端与源码控制流把直接异常链闭合为 `_attribute_first_answer_failure → _diagnose_codex_boundaries → longmemeval_s.run.AdapterError`：即首答归因中的 LongMemEval 能力调用失败；终态只保存异常类型且随后销毁归因 scratch，因此现有证据不能继续区分 Reader、Judge、Schema 校验或 transport 子类型，禁止猜测。终态 `88985de6…bb87`（文件 SHA-256 `92c0af3c…4bc6`）据此记录 `evaluation-process-error`、`candidate_decision=null`、`candidate_failure=false`，没有运行 V0，也没有把评估器异常洗成候选失败。可逆内容和 scratch 已销毁，正式 state 未写且仍为 `0587d7f9…da01`；全新进程只凭 plan identity 返回 `reused`、模型 0、产品执行 0、直接依赖有效。该内容禁止恢复或原样重跑；Stage 2 只重开首答归因评估流程，5/15/25 题、Stage 3—5 与候选身份不失效。

提交 `aaf283c` 后的全新 50 题计划 `c45a62f1…5e90` 由 8 个隔离单-turn worker 首轮生成 50 项，完整准入拒绝 4 项后只替换这些项，并在第二轮将最终集合审为 50/50；生成/准入调用 `54/2`，候选调用 150 次，全部零重试、零限流、零中断、零 worker 重启。候选 observation `aeb6fe93…30f` 先于归因耐久保存：首答 `49/50`、事实交付 50/50、时间/冲突 `0.9/1.0`、检索 p95 `375 ms`；绝对门因首答与时间失败。单题归因按冻结原序只选第 37 项，产品上下文与完整 oracle 上下文均为 3/3 机械失败，Judge 正确/错误对照各 1/1 通过，因此首个可证边界是 oracle Reader 评测能力本身不能稳定回答该独立材料，不得把它记为候选失败，V0 也未运行。归因用时 `32.903 s`，总判定墙钟 `1339.815<1961 s`。终态生成器遗漏显式 `fail_closed` 字段导致首次零执行恢复被验证器拒绝；迁移收据 `f49134de…1d76` 只补上已由 status、failure transition 与验证器共同要求的该字段，保留原终态身份/摘要 `36cfb478…09b` / `d0b2128a…56f1` 供审计，候选 observation 未重写，模型/产品执行均为 0。终态现为 `0fdfe34e…f468`（SHA-256 `1ed51f1c…2255`），按 plan identity 恢复模型/产品执行均为 0；原始材料和 scratch 已销毁，正式 state 仍为 `0587d7f9…da01`。该内容禁止恢复或原样重跑，Stage 2 只重开 oracle Reader 评测能力，5/15/25、Stage 3—5 与候选身份继续有效。

Stage 2 的当前合同 `fd036e2e…3e62` 先封存上述根因边界：六个 Reader 输出均通过 Schema，传输零超时、零限流和零 worker 重启，Judge 正误对照通过；在产品与完整 oracle 同时 3/3 失败后，首个可证偏离点是 Luna/xhigh Reader 的语义裁决，不是候选证据、官方 renderer、Judge 或 transport。合同不改产品首答 Luna/xhigh，只将失败后的诊断 Reader 冻结为 Terra/high；`_answer_prompt`、官方 Judge renderer、Schema、Terra/medium Judge 和重复次数保持不变。与盲测、正式集及开发/回归事实不重合的三题材料 `76de133f…d538` 覆盖时间更新、权威冲突和跨证据组合；资格 plan/result `be30d6a0…128f3` / `885192c9…1284` 实际完成 15 次 Reader、24 次 Judge，产品与 oracle 上下文均零失败，Judge 正误对照各 3/3，零限流、零 worker 重启，墙钟 `64.332 s`，加归因前保守上界为 `1751.056<1961 s`。候选与产品执行均为 0，scratch 已销毁，正式 state 前后均为 `0587d7f9…da01`；新进程按同 plan 恢复为零模型、零产品执行。迁移收据 `311cce09…6dc3` 只更新失败后评测能力直接依赖，不重写 5/15/25、Stage 3—5、V2 候选或三次失败 50 题的既有事实。

该资格提交后的全新 50 题计划 `58bdcf58…6cdc6` / 结果 `a4936f6d…750d` 首轮生成 50 项，质量准入只拒绝 3 项；控制器保留其余 47 项、局部生成 3 个替代项并在第二轮对完整集合重新准入，最终五类覆盖各 10 题且全部质量检查 50/50。候选 150 次 Codex 调用全部首次成功，首答 `49/50`、事实交付 `50/50`、时间/冲突 `0.9/1.0`、完整消费者检索 p95 `359 ms`；绝对门失败后没有运行 V0。冻结原序第 37 项的 Terra/high 单题归因中，产品上下文 3/3 失败、完整 oracle 3/3 正确，Judge 正误对照各 1/1 通过，故首个可证边界为 `candidate-context-failure`，不是评测流程失败。候选判定墙钟 `1538.215<1961 s`；生成/准入/候选均零限流、零中断，传输 worker 零重启。终态只保留不可逆聚合与身份，原始题面、真值、答案、逐题输出和 scratch 均已销毁；plan identity 恢复模型/产品执行均为 0，结果 SHA-256 `69baaed8…8134` 与正式 state `0587d7f9…da01` 逐字不变。该内容禁止复用，Stage 3 据此重开独立问题链。

Stage 3 重开链没有恢复、推断或复用上述盲测内容。独立合同 `314c06af…dded` 先冻结诊断/确认职责、8 次读取、24000 字符和零正式写入边界；长资产根因复现材料/输入 `34d8bb1f…c6e2` / `b46e6964…17ece` 又在候选改动前单独封存。原冻结 subject 在这两条独立长资产材料上的结果 `1e101242…caca` 为 0/2。逐层轨迹表明目标来源已返回并读取，细粒度 evidence 从约第 500 rune 开始，Reader 收到的片段缺少来源首部中的时间与修订元数据。首次 candidate access 返回的首部内容、revision 与不重叠区间本身正确，但 JSON 的零起点因 `omitempty` 被省略，Python 绑定校验以 `Ownward evidence returned a non-source-bound prelude` 失败；修复只在 candidate-only access 响应中显式封存零起点，消费者仍严格校验同 revision、首部结束不超过 evidence 起点并把首部计入原上下文预算。最终 subject `edfeb11b…57d`、内核世代 `aa4be7b8…77ea`、内核效果 `a2ad626f…f922`、二进制 `54dafaaf…7795` 和候选收据 `fa4d34ec…4831` 由当前源码、access transform 与直接依赖共同寻址。原两题执行 `89158b42…aba8` 为 2/2，结果前冻结的三题确认执行 `9c88f70e…b326` 为 3/3；两组事实交付均完整，读取和上下文上限未变，15 次 Codex 调用零重试、零限流、零中断、零 worker 重启。同一 plan `bb9a77ca…9cd1` / `23e98abb…6ba3` 恢复均返回 `reused_execution=true`，没有模型或产品执行；正式 state 仍为 `0587d7f9…da01`。

修正后的全新 5 题计划 `82cfab1b…c2b9` / 结果 `b13bf7c1…60bd` 首批 5/5 通过独立准入；V2 先通过绝对门后才运行同材料 V0，双方最终回答、事实交付、时间与冲突均为 5/5。V2 完整消费者检索 p95 `172 ms`、semantic input `21590<=27948` Token、产品数据 `135104<=367529 B`；候选判定墙钟 `321.619<=406 s`。生成/准入调用 `5/1`，候选/V0 各 15 次 Codex 调用，零重试、零限流、零中断、零 worker 重启。全部可逆内容和运行 scratch 已销毁；仅凭 plan identity 的全新进程恢复为零模型、零产品执行，正式 state 仍为 `0587d7f9…da01`。该计划已经作为本轮全新 15 题的有效前级使用，现仅保留为最后通过关卡的历史证据。

旧 15 题计划 `ac540d14…3b6c` 以该有效 5 题结果为前级；attempt 1 完成 15 次生成和 1 次质量准入后有 1 项被拒，原始批次已销毁，不可逆聚合 SHA-256 为 `1405105b…242a`，候选/V0 执行均为 0。合同 `maximum_admission_batches=3` 证明单批拒绝原应继续全新批次，只有三批耗尽才属于 Stage 2 可靠性失败。上一轮误分类清理销毁了唯一随机恢复秘密；该未终态计划因此按协调裁决失效，不计关卡结果。新的 15 题计划必须从有效 5 题前级以全新 seed 和材料开始，不是候选失败后的原样重测。

全新 15 题计划 `c74ed563…52f5` / 结果 `3ce4bb4e…b1f7` 首批准入且没有拒绝批次。V2 绝对门与同材料 V0 均为 15/15，事实交付缺失为 0，时间与冲突正确率均为 1.0；V2/V0 semantic input 为 `65087/84587` Token，Ownward 数据为 `408031/1105570 B`，V2 完整消费者检索 p95 为 `141 ms`。生成/准入调用 `15/1`，生成有 2 次结构重试、零限流；V2/V0 各 45 次 Codex 调用，零重试、零限流、零 worker 重启。唯一失败是累计判定墙钟 `833.2785687>751 s`，终态将首个通用方向标为 `execution-or-resource-boundary` 且要求独立不重合复现。全部可逆内容与 scratch 已销毁；plan identity 恢复逐字复用，零模型、零产品执行，正式 state 仍为 `0587d7f9…da01`。

阶段 3 墙钟归因合同/结果为 `0015fcc6…23b1` / `72d6f045…aaef`。它不读取或重建上述盲测内容，只使用不可逆聚合、独立 Stage 2 资格收据与既有执行诊断：旧关卡的生成+准入占 `510.581 s`，候选完整消费者仅占 `135.312 s` 且质量全过，V0 与共享外部能力不是候选内核成本；同一生成/准入职责在单个持久通道的两批独立 15 题为 `409.974/426.684 s`。旧控制器实际为 16 个逻辑调用逐个创建 App Server 并串行生成，故原 `relative-rejected` 被重分类为评测流程失败，原始结果仍保持不可改写。新控制器以 4 个隔离 worker 有界并发生成，每 worker 最多一个 turn，失败开放且按原序提交；候选与 V0 的同一四路传输已实际实现 `3.414/2.815` 倍聚合墙钟重叠，因此投影保守只承认 2 倍。保留其余 `322.697 s`、最慢独立生成 `394.921/2 s`、最慢准入 `31.534 s` 和重复误差后为 `568.401 s`，原 `751 s` 门未放宽。该变化只影响 Stage 6 控制器和合同；V2 subject、内核世代、Stage 4 证据、Stage 5 冻结合同与四个检查点直接依赖均不变，故精确复用而非重跑。

历史控制器的关卡链为：5 题计划/结果 `e7b74e15…305e` / `6411e35e…539`，15 题 `cdae1f16…27db8` / `fa321155…b502`，25 题 `4fc7be4b…415b` / `c4e64c74…3723`；25 题为 24/25，事实交付缺失 0、时间与冲突正确率 1.0，首缺口为一项 `evidence_read_answer_incorrect`，V0 与 50 题未运行。独立诊断合同/终态为 `9be7e22d…bffd` / `767654c6…0fad`：候选与 V0 的 5 个诊断、4 个回归全部通过，观察器重放一致；Reader 的产品上下文 15 次回答无机械错误但 4/5 案例存在输出变化，oracle 上下文 10 次回答有 2 次机械错误；Judge 正误对照均为 5/5，并接受 23/25 Reader 输出，与两次 Reader 错误闭合。该证据不含盲测内容、没有形成候选决定，也没有授权修改冻结 Reader；它只证明内核上下文无缺失并重闭 Stage 3。拒绝批次终态现保存覆盖、检查、组合等不可逆聚合；该控制器直接依赖变化使历史关卡全部退出当前有效链，下一次必须从全新 5 题开始。

本次 Stage 3 修复链的实现验证为：全量 Go test/build/vet、LongMemEval-S 适配器 44/44、候选与合同定向测试 6/6、Suite `check` 与 `self-check` 通过。Acceptance 全量单元测试为 323/337 通过；其余 14 项全部在 Stage 4 冻结依赖校验处因当前 `run.py` 执行身份漂移而失败关闭，没有产品、Schema 或评分断言失败。该结果是旧 Stage 4/5 证据不能静默映射到当前 subject 的机械证明，不授权本链修改迁移收据或重跑高成本层；应由下一工作包按真实消费者依赖精确重证。

25 题失败后的 Stage 3 重开链只接收不可逆聚合边界，没有恢复或推断盲测事实。合同 `8aac10af…f11` 在候选改动前封存两题诊断、两题事实不重合确认、三次重复 Reader、Judge 对照、8 次读取与 24,000 字符上限。旧 subject 的根因复现 plan/result `93ce3407…694c` / `bd602db3…653` 为产品上下文 0/6、完整来源 6/6；真实轨迹证明目标来源已返回并读取，但每来源三项 evidence 之后仍有第四个相隔较远的必要范围未交付。当前 subject `a0a59771…117`、世代 `75a460c2…8aee`、内核效果 `a2ad626f…f922`、组合 `19b9b25d…85c3`、二进制 `9fbadbde…42c5` 和候选收据 `fcbdb44e…dcad` 只在 candidate access 与消费者层增加 `limit+1` 截断证明；仅当同 revision 完整来源可装入剩余预算时，以一个读取单位交付完整来源，否则保持原细粒度路径。v3 终态 plan/result `68e63877…7757` / `d3f5abf3…4d6a` 将 `bd602db3…653` 已证的 `kernel-context` 原根因作为不可变 `root_cause`，并把当前候选不再复现、Reader/Judge 稳定、三项执行及观察器重放通过单独封存在 `repair_validation`；旧 v2 终态 `113253f6…1960` 因混淆这两类事实只保留历史诊断资格。诊断与独立确认均为产品/完整来源 6/6，Judge 接受 24/24 Reader 输出并通过正误对照；根因轨迹最大读取 `6/8`、上下文 `5705/24000` 字符。既有开发 `e29d2b3f…d6a` 为 4/4，固定回归 `22a606b2…cde` 为 8/8，事实交付均完整；v3 终态由已有检查点生成且记录零模型 turn、零产品执行。正式 state 前后保持 `1d15b267…42eb`。全量 Go test/build/vet、LongMemEval-S 适配器 43/43、Suite `check`/`self-check` 通过；Acceptance 324/338，通过外的 14 项均在旧 Stage 4 冻结依赖迁移收据处因当前执行器身份变化失败关闭，证明下一工作包必须精确重证 Stage 4/5，而不能在本链洗白旧证据。

当前 Stage 4 精确迁移 `fd82621e…998` 只把 source-context 变化归入消费者质量/时延重证，原成本合同 `db0de5be…a927`、受控成本 `3e06bfd3…fff72` 与语义成本 `c7480729…5a1b` 保持原始身份。当前开发 `e29d2b3f…d6a` 为 4/4、回归 `22a606b2…cde` 为 8/8，事实交付完整，p95 分别为 `250/359 ms`；迁移定向测试 44/44 通过。Stage 4 因此绑定当前 subject 重新关闭，没有重新调用模型、改写门槛或重做仍有效证据。

Stage 5 合同 `968f71e…22d` 与组件清单 `48d155fa…bd5` 在新内部结果前冻结。干净 HEAD `a74a2e1…65e9` 重建包装 subject `04abd7be…a7b`，runtime `75a460c2…8aee`、内核效果 `a2ad626f…f922`、组合 `19b9b25d…85c3` 与当前候选一致；二进制 SHA-256 `4004f63f…b46a`、frontier `322879d0…0dd3`，freeze 身份 `e43e5666…599`。正常 rebind 只失效 product 依赖的四层并保留三条 V0 基线历史；frontier/core 分别以 `4.513/35.571 s` 通过，报告 SHA-256 为 `ed4d35c1…d886` / `be6c60eb…40deb`。

qualification 首次执行在 `1566 s` 硬上限处失败开放；机械核对确认 s01—s04 已封存、s05/s10 已有完整 agent 结果、s06/s11 分别保留 2/5 与 4/5 semantic 进度，没有场景需要从零重做。同一正式 `--resume` 保持上述六份既有终态字节不变，仅补真实缺口，并在 `393.766 s` 记录 8/8；报告 SHA-256 为 `0813929d…62b2`。full 直接复用这 8 项后只执行其余 16 项，在 `4453.211 s` 完成 24/24；报告 SHA-256 为 `4a1c4d98…24fd`，四类各 6 题的 recall、precision、NDCG 均为 1.0，Ownward 查询最大 `290.555 ms`、资源峰值 `251.512 MiB`。完整证据树记录 7 个 `no-successful-semantic-submit` 的有界失败尝试及首次硬停止留下的 2 个 interruption 检查点；对应工作均由原位重试或精确恢复成功，没有静默降级、限流或 worker 重启。

终态 frontier/core/qualification/full 的同身份 `--resume` 均返回 `reused`；state SHA-256 前后保持 `b299ce4f…1c6f`，597 文件的场景证据树摘要前后保持 `ca3ac3ce…5bf4`，证明零模型、零产品执行的精确复用。frontier 绑定干净源码提交 `a74a2e1…65e9`，core/qualification/full 绑定 runtime `75a460c2…8aee` 与二进制 `4004f63f…b46a`；四层均绑定同一内核效果 `a2ad626f…f922` 和当前环境，三条 V0 基线历史身份逐项保持不变。Stage 5 因此关闭；当前候选尚未生成或运行任何 Stage 6 关卡，也未运行正式 LongMemEval-S。

当前 Stage 6 四级合同只参数化到当前 source subject `a0a59771…117`、runtime `75a460c2…8aee`、内核效果 `a2ad626f…f922`、Stage 5 合同 `968f71ee…c22d` 及当前 Reader 选择 `268023d6…f8fe`；合同身份依次为 `9dea6f52…40c5`、`bcf347a6…f9c0`、`cc5f5f4a…f701`、`4ba1de71…ca48`。四级统一使用同一局部替换调度：仅重生先验拒绝项、保留合格项、每轮对完整冻结集合重新准入，最大轮数仍由原级别预算约束；不存在 5/15/25 整批重生合格项的同义路径。Stage 5 自包含执行 subject 为 `04abd7be…0a7b`，候选/V0 共享条件身份为 `08d241ab…396b`；既有 Terra/xhigh 失败归因资格 `c58dd793…fe5d` 通过精确迁移继续有效，没有重跑模型资格。

全新 5 题计划/结果为 `b07facf0…04c7` / `8d627301…3464`：候选和 V0 均 5/5、事实交付完整、时间/冲突正确率 1.0，候选检索 p95 `234 ms`。旧 15 题计划/结果 `3d418feb…ad3` / `7d67ea03…e709` 保持不可改写的 `evaluation-process-error` / `candidate_decision=null`；裁决 `stage6-candidate-decision-adjudication.json`（身份 `4c34f5dc…e3a1`）只确认其 `1000 ms` 是非超时单尾，并以同依赖 60 请求 p95 `533.533<=553 ms` 判定性能通过，答案责任与候选结论均未知。两次全新 15 题 `aa1ad770…73e`、`2f9a34b1…cb0b` 进一步证明评测 Reader 存在首答随机与完整 oracle 不稳定，均保留为流程失败历史。

通用归因合同现在要求：只有原首答失败、同一产品上下文的两次已资格化诊断 Reader 都正确、完整 oracle 与 Judge 正误对照稳定时，才把该项归为外部 Reader 首答随机并只从派生候选决定中移除 `final_answer_accuracy`；原 report/answer 仍保持失败字节，事实交付、时间/冲突和性能不被改写。候选上下文稳定失败仍归候选，oracle/Judge 不稳定仍失败开放。历史 5/15 及两份流程失败结果均经迁移后仅凭 plan identity 零模型、零产品执行恢复。

在该边界下，全新 15 题计划/结果 `4f65af5b…32d` / `dfeeae6e…f064` 真实通过：候选 15/15、V0 14/15，候选事实交付完整、时间/冲突正确率 1.0、检索 p95 `453 ms`；候选/V0 各 45 次 Codex 调用，零重试、零限流、零中断、零 worker 重启，恢复的 report/checkpoint 逐字一致且零执行。其累计运行墙钟含一个被准入拒绝并局部替换的材料轮次，但候选判定墙钟 `451.179<=751 s`，拒绝材料没有计作候选失败。

随后全新 25 题计划/结果 `c204269d…fe91` / `fb212b78…9ef` 在 25/25 先验准入后运行候选并按绝对门停止：事实交付 25/25、检索 p95 `500<=553 ms`、冲突正确率 1.0，但最终答案 24/25、时间正确率 `0.8<1.0`。失败答案的产品与完整 oracle 三次诊断均机械失败，故答案责任仍为评测流程未知；这不影响独立时间正确率绝对门，候选据此真实拒绝，V0 和 50 题没有运行。候选 75 次 Codex 调用零重试、零限流、零中断、零 worker 重启，候选判定墙钟 `897.145<=1097 s`；原始内容和 scratch 已销毁，同身份终态恢复零模型、零产品执行，正式 state 仍为 `b299ce4f…1c6f`。

时间正确性定向裁决只接收上述不可逆聚合，不恢复、推断或转写盲测内容。两份结果前封存的独立材料 `7507f704…39b0`、`88a95ddd…f6db` 覆盖未来生效边界、追溯更正、相邻有效期、记录时间与事件时间分离、as-of supersession、单日例外、前瞻与追溯并存及作用域重叠；后者每题 10 个返回来源竞争固定 8 次读取。当前候选执行 plan/result 为 `2404299a…62a5` / `80b61157…8047` 与 `f19b4059…acf4` / `536834cf…6d1b`：9/9 时间答案正确，21/21 必要事实全部搜索返回并读取，最大必要来源排名为 6，首缺口均为 `none`，检索 p95 分别为 `265/125 ms`；27 次 Codex 调用零重试、限流、中断和 worker 重启。同身份恢复保持两份结果逐字不变且零模型、零产品执行，正式 state 前后为 `b299ce4f…1c6f`。机器裁决 `iteration/v2/stage3-temporal-correctness-adjudication.json`（身份 `c64ccb97…91d7`）据此记录 `not-reproduced-with-independent-counterevidence`：信息组织、查询融合、来源排序、时间适用性所需原文和交付链均未出现首个偏离节点，不能授权猜测式内核修改；25 题候选拒绝仍保持不可改写，责任边界继续未知。

15 题检索尾部的独立复现合同在任何新测量前冻结：只使用与盲测事实不重合的既有真实规模材料 `e9ecd0be…a330`，覆盖 `38/46/54/62` 个资产、正式 `24` 搜索/`8` 读取/`24000` 字符和四个并发产品进程；候选二进制 SHA-256 为 `4004f63f…b46a`，EmbeddingGemma 清单为 `fc2e4a1a…c0da`，协议为 `6a52f479…57f`，持久 MCP 传输为 `fbca3e4f…1a86`。复现只从已封存 prepared data `715331e2…71b9` 的隔离副本运行三轮，每轮一次预热和五次测量，共 60 个完整消费者请求；不调用模型、Reader、Judge 或 V0。每个请求分别记录 Search、Evidence Search、Read、协议开销、调用数、上下文字数和工作集；选择轨迹、必要来源交付、prepared source 与正式 state 必须逐字不变。判定门在结果前固定为 p95 `<=553 ms`；只有重复出现且可归属同一产品阶段的尾部才授权候选修复，未复现或只能定位到机器级瞬态竞争时不得修改候选。

复现结果已登记进同一裁决收据：60 请求总体 mean/p95/max 为 `397.147/533.533/633.301 ms`，三轮 p95 为 `443.787/513.504/569.436 ms`；四个各 15 样本分组中，`46/38` 资产两组因单个尾部达到 `633.301/569.436 ms` 而越过 `553 ms`。最高尾部中 Search/Evidence Search/Read/协议分别为 `519.008/54.445/59.847/1.073 ms`，所以首个可证断点是四 worker 负载下的 `ownward_search`，不是证据搜索、读取或协议。总体 p95 仍通过，选择轨迹、目标交付、prepared source 和正式 state 逐字不变，隔离副本已销毁；证据因此只证明小样本单尾可复现，没有证明系统性候选回归或 Search 内更深产品根因，不授权修改候选。

## 调度闭环

协调者每次开始、恢复或收到执行者通知后，读取本文、目标对话最新历史、仓库状态和机器证据，选择最靠前且未满足依赖中的最高价值问题链。每个工作包必须写清：

- 当前检查点与可证根因；
- 负责方向、目标指标和预先冻结的通过门槛；
- 必须保护的 V0 能力、经同尺重证且与失败机制可分离的 V1 收益，以及不在本包范围的事项；
- 为消除根因所需的完整实现边界、可复用证据、成本上限和失败退出条件；
- 完成后应更新的本文状态、机器证据和下一验证点。

执行者在边界内自主选择最优实现，不得以最小改动代替最优结果，也不得扩大问题链。协调者独立复核实现、证据、任务状态和 Git 边界；未满足门槛则续做、换路或淘汰候选，真实关闭后才提交并进入下一工作包。普通技术问题不得转交用户。

## 验证闭环

1. **归因**：只从 V1 的聚合结果与已确认通用缺陷构建问题池；沿完整链路以独立案例证明首个根因，不得把相关现象直接写成根因。
2. **开发**：在根因确定后建立少量、真实、结构完整且不含正式测试内容的端到端案例；单轮开发与受影响验证目标不超过 10 分钟。
3. **回归**：按 V0 已正确能力的任务类型、复杂度、关键结构和风险分层独立构造固定材料，并纳入经同尺重证且与失败机制可分离的 V1 收益；覆盖配额和选择规则在运行 V0 前冻结，再按规则机械形成保护与提升分层。不得复制正式题目、挑选容易保护的样本或依据候选结果改变材料。候选每次修改只重跑受影响部分，冻结前完整运行。
4. **整体验证**：只有开发与回归已显示大幅提升的候选才运行；同一候选必须同时通过内核、core、产品、恢复、性能与资源保护。
5. **盲测**：候选冻结后，由独立流程临时随机生成并先验准入全新高质量数据，依次运行 5、15、25、50 题。每级候选先跑，达到绝对门槛后再跑同数据 V0；判定不可逆时立即停止。
6. **失败反馈**：每次失败必须产出首个可泛化根因、负责方向、受影响证据和下一验证点；盲测内容使用后立即销毁，不得固化进开发集或回归集。

盲测正式启用前的历史非候选五题校准冻结了 5/15/25/50 题正常预算 `406/751/1097/1961` 秒、失败预算 `320/492/665/1097` 秒，正常总计 `4215` 秒，低于 `9000` 秒设计上限；其原始内容已销毁且不参与候选判断。当前 validation `059ea0ab…ccb6` 的两批 15 题非候选资格以 `301.525/258.295 s`、总计 `559.820/984 s` 证明 8 路局部替换调度无需放宽预算。预算身份 `07bb9639…aaf` 继续绑定 Terra/xhigh 生成、Terra/medium 准入和正式/Stage 6 共用的 Luna/xhigh Reader；四级原投影与硬门均未改写。当前资格终态恢复没有模型或产品执行，旧资格与拒绝批次只作已销毁历史诊断。

## 状态与恢复

本文只维护唯一调度状态，机器事实与检查点仍由 Acceptance Suite 保存，本文引用其身份和结论，不复制证据或建立平行真相。每个工作包结束时据实更新：

| 字段 | 当前值 |
| --- | --- |
| 当前阶段 | Stage 4 与 Stage 5 继续有效，full 为 24/24。Stage 6 全新 15 题已通过；全新 25 题因独立时间正确率绝对门失败真实拒绝候选并停止，50 题未运行，正式 community 继续禁止 |
| 评测基线 | V0 世代 `1952f4b8…7b77`、映射内核效果 `61729fb8…017a`；唯一同尺比较合同为 `iteration/v2/comparison-contract.json` |
| 当前产品 | 组合 `c068ae20…751e`、内核 `db18a3a1…2684`；保持 V1 行为，不是有效评测基线 |
| 正式状态 | 当前 Acceptance binding/state 为 v6/v3，已按正常生命周期绑定 Stage 5 当前候选；frontier/core/qualification/full 四个检查点有效，三条 V0 基线历史保持不变，state SHA-256 为 `b299ce4f…1c6f`。Stage 6 非正式证据没有写入该 state，community 未执行 |
| 迭代合同 | `ownward.kernel-iteration-comparison/v3` 政策修订 `b5400416…198b` 与 `ownward.kernel-iteration-baseline-facts/v1` 已冻结且不依赖 `.tmp`；旧 `b1e3b07a…fbbe` 只作为未受时延纠正影响证据的兼容身份，V0、当前产品和 V2 subject 独立，Git 仅作审计来源 |
| V2 候选 | 当前候选 subject `a0a59771…117`、内核世代 `75a460c2…8aee`、内核效果 `a2ad626f…f922`、封存组合 `19b9b25d…85c3`；干净 HEAD 重建包装 subject `04abd7be…a7b`、二进制 SHA-256 `4004f63f…b46a`。包装与审计来源变化没有改变能力世代；当前产品与 V0/V1 制品未切换 |
| 当前根因 | 全新 25 题事实交付与检索性能均通过，但时间正确率 `0.8<1.0` 独立失败；该候选拒绝继续有效。定向裁决 `c64ccb97…91d7` 的 9 个独立时间案例、60 个来源、21 项必要事实均无组织、排序、适用性或交付偏离且答案 9/9，因此尚无首个可证内核根因，不能把盲测聚合失败直接归因给内核或据此修改候选 |
| 方向状态 | 最终回答充分性问题链已在正确的 access/消费者层关闭；内核效果身份未变。Stage 4 当前候选重证和 Stage 5 内部整体验证均已完成；尚未通过 Stage 6，不提前关闭大版本 |
| 保护边界 | V0 固定回归 8/8 且事实交付完整；V1 长资产语义召回与批量耐久已同尺重证并可分离，细粒度证据和存储收敛未达到保护条件 |
| 冻结门槛 | 原质量与检索门继续成立；候选决策要求 semantic 指令+payload `<=10247.5` Token、候选可控本地组件加重复误差 `<=6.711907 s`、真实 Ownward data bytes 为 V0 的 `<=0.5`。当前分别为 `9674`、`1.485877 s` 和 `0.401517`；测试克隆、日志、报告和检查点继续单列，三个维度没有互相补偿 |
| 盲测级别 | 有效 5 题 `b07facf0…04c7` 继续通过；全新 15 题 `4f65af5b…32d` / `dfeeae6e…f064` 已通过；全新 25 题 `c204269d…fe91` / `fb212b78…9ef` 候选失败并停止，V0 与 50 题未运行。旧 15 题及两份后续 Reader 流程失败只作不可改写历史；全部原始内容已销毁 |
| 当前证据 | Stage 4 迁移 `fd82621e…998`、开发 `e29d2b3f…d6a` 4/4、回归 `22a606b2…cde` 8/8；Stage 5 freeze `e43e5666…599`，frontier/core/qualification/full 报告 `ed4d35c1…d886` / `be6c60eb…40deb` / `0813929d…62b2` / `4a1c4d98…24fd`，full 24/24；Stage 6 历史 `0f72d3c6…2d43` 登记全新 15 题通过与 25 题候选失败；时间定向裁决 `c64ccb97…91d7` 绑定两份独立材料和两份零缺口执行结果。所有终态按 plan identity 零执行复用，正式 state 未写入 |
| 下一验证点 | 停止并由协调者复核 `c64ccb97…91d7` 的非复现裁决；在没有新的独立、可复现内核偏离证据前，不修改 V2 内核、不失效 Stage 3—5，也不生成任何新盲测关卡。不得进入 50 题、community preflight 或正式 LongMemEval-S |

恢复时只复用身份和直接依赖仍有效的结果。V2 代码或专属直接依赖变化只失效相应的 V2 证据；共享评测数据、工具、模型、提示词、评分器、配置或环境变化，只失效依赖该条件的 V2 比较证据。V0/V1 的制品、运行能力和各自在原合同下成立的既有证据不因 V2 变化而失效；在新数据或新评测合同下运行 V0 只是在该条件下建立对照，不是重验或改写 V0。候选冻结后，V2 或任一冻结直接依赖变化必须重开阶段 5，并使该 V2 的全部盲测关卡失效。相同身份的中断可以从原子检查点继续；文档和无关项目变更不得迫使任何稳定内核重测。

## 结束条件

以下条件必须在同一冻结 V2 候选上全部成立：

- V0、当前产品与 V2 的身份职责明确；V0 与 V2 保持不同内核身份及各自专属制品和直接依赖，只共享一致且可复现的评测数据、工具、模型、提示词、评分器、配置和环境，使内核版本成为唯一预定变量；重复测量误差已排除，V0/V1 仍可独立运行，其既有证据未被改写或重新证明。
- 唯一 V2 迭代合同已在任何候选结果产生前冻结整体跃升门槛、不可退化能力、证据职责、失效图和成本边界；V0 历史结果未被改写，V2 只在阶段 5 后通过 Acceptance 正常生命周期形成自身内部检查点。
- 问题池、端到端开发集、代表性固定回归、整体验证和一次性盲测均已实现、测试、校准并具有唯一入口与原子恢复点；现有 V1 专用合成视图不承担 V2 关闭或泛化证明。
- 五个方向均已审视并以“优化”或“保护”的证据关闭；真实瓶颈完成优化，非瓶颈方向的既有能力得到保护，没有为完成清单强行修改。
- V1 已知收益均已在同尺条件下完成重证；与失败机制可分离的真实收益由 V2 保持或提高，其余收益具有明确拒绝证据，V1 未被建立为第二基线。
- 候选分别达到预先冻结的信息组织质量、检索与最终回答质量、端到端效率跃升门槛；只在基线存在提升空间的指标上要求提升，高基线指标保持，任何维度均无实质退化或跨维度抵消。
- 固定回归、完整内部验收、恢复与失败开放均通过；局部代理或单一数据集未被用作完成证明。
- 5、15、25、50 题一次性盲测依次通过；数据先验质量合格、与已有数据不重合、使用后销毁，累计证据证明未知数据泛化和 V0 能力保护。
- 检查点、直接依赖失效和中断恢复已验证；没有重复准备、无效高成本重跑、双轨状态或遗留兼容路径。
- 正式 LongMemEval-S 尚未运行，V2 尚未晋升；验收对象已绑定同一冻结 V2，配置、恢复入口、预算和证据摘要已通过不运行正式题目的预检，可由唯一下一动作直接启动；该验收绑定不改变产品当前内核或其他内核版本。

全部成立后，将阶段 7 标记为完成，停止派发工作包并向用户报告“V2 已达到运行最终测试集标准”。任何条件未满足都继续调度，不得以内部满分、局部改善、基本可用或后续补齐结束。

## 用户提示词

### 持续收敛 V2 内核大版本迭代任务

```text
目标：持续审查并收敛 `docs/tasks/kernel-v2-major-iteration.md`，直到现有持续调度体系能够仅依据该任务文档，与执行者高度自主地完成一次真实、完整、高效的 V2 内核大版本迭代，并使同一冻结候选达到可运行最终 LongMemEval-S 的标准。

`docs/engineering/kernel-evolution-system.md` 是迭代方法的唯一依据，已经确认的优化范围、证据分层、一次性盲测和完成边界不得改义；`docs/incidents/kernel-v1-final-benchmark-quality-gap.md` 是必须避免重现的失败依据；`docs/engineering/kernel-versions/v0.md` 和 `docs/engineering/kernel-versions/v1.md` 分别规定有效基线与失败候选事实；产品、架构、验收和调度边界分别以 `docs/product/requirements.md`、`docs/architecture/overview.md`、`docs/delivery/acceptance-system.md` 和 `docs/workbench/development-workbench.md` 中“持续协调项目推进”为依据。当前实现、Acceptance Suite 状态、测试资产、已有证据和实测成本只用于证明任务真实可行，不得反向降低目标或把尚未实现的能力冒充现状。

只允许修改 `docs/tasks/kernel-v2-major-iteration.md`。本文“用户提示词”是用户执行入口，必须保留；发现真实缺陷时可以修正，不得在删减中删除。不得修改上述依据、代码、测试资产或运行状态，不得调度目标对话、实施内核优化、生成盲测数据、运行验收或执行 Git 操作。

每次开始、恢复或上下文压缩后，重新完整读取上述文件和本任务文档，检查 Git 状态，并依据仓库事实继续。持续执行以下循环：

1. 从预期结果反推任务完整性。执行本文全部阶段后，必须得到相对 V0 在信息组织质量、检索与最终回答质量、端到端效率上按基线提升空间分别形成真实大幅跃升、V0 已有正确能力无实质退化、通过完整回归与四级一次性盲测的同一冻结 V2 候选；三个维度不得相互补偿，任何只完成流程、实现机制、提高局部指标或形成小幅净收益便可结束的路径都必须修复。
2. 审查起点是否真实。V0 必须是唯一有效评测基线，V1 只能提供诊断、失败证据和待重新证明的实现与收益；不得把当前产品实现、Acceptance state 的 `baseline=null` 与 V0 评测基线混为一谈，也不得未经同尺重证便保留或丢弃 V1 收益。任务必须能够从当前仓库、组件与组合身份、Acceptance Suite 状态和有效检查点机械恢复，不依赖过期提交绑定、隐含背景或目标对话自报。
3. 审查阶段与依赖是否完整。比较合同、验证能力、端到端归因、开发与回归材料冻结、五方向审视、候选优化、完整回归、内部整体验证、候选冻结、5/15/25/50 题一次性盲测、失败回退和最终测试交接都必须拥有清楚的输入、输出、状态、证据、关闭条件和唯一下一动作；验证机制不得要求在根因确认前猜测具体开发材料，不得出现无法派发、无法复核、无法恢复或提前结束的断点。
4. 审查自主调度是否真实可运行。协调者必须能依据唯一任务状态选择当前最高价值问题链，形成边界清楚的工作包，独立复核结果并决定续做、换路、淘汰、提交或进入下一阶段；执行者必须能在工作包内自主取得事实、选择最优实现并完成验证，无需猜测隐含要求。普通技术问题不得转交用户，任务状态不得与机器证据形成平行真相。
5. 审查效果与抗拟合证据。开发集必须提供分钟级真实端到端反馈，固定回归必须按 V0 已正确能力的类型、复杂度和关键结构代表性覆盖，并保护经同尺重证且与失败机制可分离的 V1 收益，局部代理只能诊断；开发与回归材料必须独立构造，正式测试内容不得进入开发、回归或盲测生成。候选冻结后的一次性盲测必须由独立流程临时随机生成全新高质量数据，先验完成质量准入，按 5、15、25、50 题逐级运行；候选裁决失败时销毁数据、保留通用根因并退回完整迭代，评测流程失败则只阻断下一关，既有候选通过事实保持不可改写，直至流程修复和独立非候选资格完成。
6. 审查任务是否能持续产生更优候选。每次失败必须形成可泛化的首个根因、负责方向、受影响证据、保护项和下一验证点，足以直接驱动下一工作包；任务不能只会拒绝坏候选，也不能通过固定样本、反复调整门槛或泄露盲测内容获得通过。
7. 审查质量与安全边界。V0、当前产品和 V2 必须保持独立内核身份；V0/V1 始终可独立运行，其制品、状态和既有证据不得因 V2 的开发、失败、验收重绑或淘汰而改变。V0 与 V2 只共享相同的评测协议和评测条件，内核版本及其专属制品与直接依赖是唯一预定变量；V2 质量、时延和资源分别达标，其证据只因自身或真实直接依赖变化而失效。任务不得混入产品功能、架构迁移、正式验收规则修改或与内核无关的工程工作。
8. 审查执行效率。依据已有机器实测和新增机制的保守成本模型核算正常路径与失败路径；日常归因、开发和受影响验证必须保持分钟级，只重跑真实失效部分并复用有效检查点。重复准备、重复验证、无效等待、串行交接、没有新证据的高成本运行和过晚淘汰都必须消除；四级盲测无法在既定质量下满足成本上限时，必须先修正执行路径，不能降低标准。
9. 从首次启动开始完整推演持续调度：建立状态、补齐迭代能力、确认根因、连续派发工作包、候选反复优化、上下文恢复、方向切换、内部冻结、盲测失败重启、四级通过及最终交接。发现职责不清、证据不足、恢复失效、状态漂移、低效路径或停止条件不成立时，直接修复任务文档并重新推演。
10. 极致删减。本文只保留会改变执行、复核、恢复或完成判断的信息；删除重复方法定义、作者心理活动、方案辩护、过程记录、实现细节和由引用文档完整承担的内容。删减不得造成执行者依赖隐含背景或协调者无法客观裁决。

准备结束时，对同一份未修改任务文档分别执行效果、抗拟合、可执行性、效率和边界安全五次独立复审：

- **效果**：全文执行完成是否必然要求信息组织质量、检索与最终回答质量、端到端效率按基线提升空间分别形成大幅跃升，保护 V0 已有正确能力，并保持或提高经同尺重证且与失败机制可分离的 V1 收益，无法以跨维度补偿、局部满分、小幅净收益或流程完成假结束。
- **抗拟合**：候选是否无法依靠正式题目、已知样本、狭窄代理、精选回归或重复盲测取得通过。
- **可执行性**：仅依据本文、引用文档和仓库事实，协调者与执行者是否能从当前状态连续完成全部阶段，每个结果是否可独立复核和恢复。
- **效率**：正常与失败路径是否沿最短可靠关键路径运行，低成本证据是否尽早淘汰无效候选，高成本验证是否只用于已有充分依据的冻结候选。
- **边界安全**：V0、权威资产、当前产品、有效证据和正式验收合同是否始终不受失败候选影响，任务是否严格停在最终测试准备完成而未擅自运行正式测试或晋升 V2。

任何一次复审发现真实问题，都继续修复并重新执行全部推演和复审。只有仓库事实证明任务起点真实，全部阶段可由持续调度体系自主执行、恢复和裁决，完成条件能够可靠筛选出真实泛化且大幅跃升的同一 V2 候选，正常与失败路径均高效，没有已知断点、隐含假设、过拟合通道或低价值复杂度时，回复“V2 内核大版本迭代任务收敛完成”，简要说明最终判断和实际修改，立即停止；不得实施任务、运行正式测试或以基本可用和后续补齐结束。
```
