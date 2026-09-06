# V2 50 题关卡失败归因（临时）

## 50 题关卡结论（2026-09-05）

V2 内核通过。c051、c064 的题目缺陷已修正并经真实入口验证正确；c047 的内核检索、目标证据返回与读取均正确，错误发生于外部 Reader 的答案组织，不属于内核问题。旧来源故障题 c055、c066、c076 全部正确。关卡不再阻止最终测试准备，也不要求重跑。

## 运行结论

- 候选：V2 `460e3f8205dab3f47c83d3620a89be447264ab81d9327e5602f06866fa4cb5fd`
- 关卡计划：`cf2f52663ebd6f5fd483cbfac2938c11c3244d8328512d262d49ed47c663ce45`
- 原始评分：45/50；原始题库、评分与失败现场保留不改。下文先记录事故原因，再记录修复验证。
- 复核结论：2 道题目与标准答案不一致；另外 3 道存在语义整理的来源混淆，叠加外部 Reader 的无效探索，最后耗尽工具调用预算。此前“2 道 Reader 答错、3 道与内核无关”的结论撤回。

## 失败题

| 题号与原题 | 冻结答案 → 实际回答 | 已核实的原因与责任边界 |
| --- | --- | --- |
| `vs-c051`：Daria is closing out Pinebridge's outreach packet after the committee's rework. What entry should appear on the authority line? | `Atlas Protocol—Revision Seven` → `Civic Handbook—Edition Nine` | **题目对象缺少证据连接。** s03、s07 只证明委员会档案对应 umbra-kite → Atlas Revision Seven；没有说明 outreach packet 采用该档案路线。s09 反而明确 outreach boilerplate → Civic Handbook Edition Nine。题目不能唯一推出冻结答案，不能据此处罚 Reader 或内核。 |
| `vs-c055`：What marker did the rollout use for work in the renewal-site corridor and for after-hours teams? | `moonstone blue` → 多组无关 marker | **题目可由 s02 的共同分配 Kestrel-19 与 s10 的颜色映射解出。** 语义结果却把各邻居的线索混入单份资产：s01 也挂入只见于 s02 的共同分配事实；s01、s02 摘要都泛化成多类任务集合。Reader 搜到目标后仍执行 7 次搜索、3 次失败导航、2 次非目标读取，未完成两跳连接。来源混杂与低效读取策略均真实存在，不能仅凭预算耗尽指定唯一责任方。 |
| `vs-c064`：At the Orchard Annex, recalling the thermal shutters' street-facing surfaces, what did I say they should receive—not the option you had put forward? | `harbor-slate varnish` → `black` | **题目把说话者写反。** s06 的 User 说自己主张 black，Assistant 说自己的决定是 harbor-slate varnish。当前用户问 “what did I say…not…you”，Reader 回答 black 符合角色关系；冻结答案取了助手的决定。前文中文转述把题意改成“最终采用什么”，掩盖了原题错误，现已纠正。 |
| `vs-c066`：For Wrenbay's evening review group that received its marker during the Lantern rollout, which text should the authorization sheet now use? | `Harbor Slate` → `Mint Quay` | **题目有效，语义摘要与资产错配。** s03 原文是 evening review → J-14，模型给它的摘要却是 day review → K-19；s08 原文是更新为 Harbor Slate，摘要却说旧值 Copper Current。目标源虽已返回且最好排第 1、3，外部看到的摘要会误导选读。Reader 又把明确 separate 的 reconciliation group 当作 review group，7 次搜索、1 次失败导航、4 次读取后答错。 |
| `vs-c076`：For the current Orchard Fund community grant, where should the Lantern pilot's payment be sent? | `North Quill Council` → `Bellwether Council`，称新值未确认 | **题目有效，更新线索挂错来源。** s11 原文是 Kite-41 → North Quill Council，摘要却成了 Juniper-06 设备资料；真正的更新摘要挂到了 s09 培训合同名下。Reader 第 3 次调用读了 s09，却得到培训合同正文；随后正确 s11 在多次搜索中排第 1，仍未被读取。6 次搜索、6 次读取后按旧记录作答。摘要与正文不一致直接破坏证据核验，不能归结为单纯调用次数不够。 |

## 共同根因与优化方案

1. **题目准入漏检语义一致性。** 题库把上述两题登记为唯一答案、证据充分，但检查未发现题目对象、说话者与真值不一致。后续只修复受影响题目的对象连接或人称，独立核对“原题—完整历史—答案”，保留原记录；不能把它们直接改判为内核通过。
2. **语义批处理没有可靠保持“内容属于哪份资产”。** 对三题的冻结输入离线重建，提示词 SHA-256 均吻合，资产索引映射正确；错配已存在于外部语义模型原始输出，提交程序按 work_id 原样接收，未发生程序二次调换。压缩的正文索引表与邻居上下文允许机器无损还原，却未保证模型正确区分目标与邻居。输出只检查 work_id、字段和顺序，没有暴露摘要依据的来源。
3. **内核检索展示放大了错误元数据。** 冻结 V2 对超过 240 字符的资产优先展示语义摘要；`c066-s03`/`c066-s08` 均 257 字符，`c076-s11` 为 252 字符，正好使用上述错误摘要。返回正确 ID 不能证明交付了可用线索。这是组织—检索衔接的真实质量缺陷；现有证据没有证明排序算法或原文读取实现错误。

**优化方向：来源明确的组织结果与检索线索。** 每项语义工作明确并列目标资产身份、正文和只供关系判断的邻居，保留有界批处理；目标摘要与事实线索必须对应自身正文，跨来源信息只能带来源引用作为关系表达。结构校验负责身份、版本和引用有效性，开放语义的忠实度通过独立样例验证，不能用字面匹配假装语义判断。检索展示优先提供可回到当前原文的短摘录，摘要仅作辅助；没有可靠来源的摘要不能替代或污染该资产的事实线索。不要以扩大 240 字符边界、换一个固定预算或按这几题特判代替修复。

验证使用资产顺序打乱、同名不同组、旧新值更新的非重合少量样例，检查“摘要归属正确—读取正文相符—主动回答正确”，同时保护已有正确能力，比较工具调用次数、端到端耗时及语义调用成本。只重建候选自己的派生结果，原始资产和旧 V2 保持完整。

## 预算与证据边界

- 三题触顶的是每题 **12 次工具调用**。`vs-c055`/`066`/`076` 成功读取分别为 2/4/6 次，读入 495/969/1564 字符，未耗尽 8 次读取或 24000 字符预算；它是失败末端现象，不是已证明的根因。Reader 提示词已经要求留出读取额度，不能再次把“加一句相同提醒”当作修复。
- 原运行 `vs-c055`/`066` 的 3/1 次导航在适配器参数校验处失败，未进入内核；当时只有参数哈希和错误，不能还原错误参数。新运行已在原有有界调用痕迹内保存实际参数与返回值，不另建诊断系统。
- 来源错配与外部策略问题可以同时存在；修复后答对不能证明外部策略已最优，也不能把每次工具调用耗尽都归因于内核。

## 修复与优化结果（2026-09-05）

新 V2 候选：`5a1de835011cd7d931b68107a387b8b57a7b47aa88489bc4ae96c376e6d41cce`；效果身份 `a8d550e95f1fddfb33d70323af4209fe5b97ba445cceeea206c82ac47c6a4615`。独立制品位于 `.tmp/kernel-v2-major-iteration/source-ownership/candidate-owned-passages/`，未覆盖或晋升旧 V2。

| 优化方向 | 实际处置与边界 |
| --- | --- |
| 信息表示与组织 | 每项工作显式绑定自身完整原文；模型选择本来源的编号片段，程序回填摘要与事实线索，越界引用拒绝。消除邻居事实挂入本资产的通道；不把原文引用等同于正确语义判断，仍验证角色、时间、条件和最终回答。 |
| 检索架构与算法 | 搜索展示改为查询相关、连续且不超过原 240 字符上限的权威原文片段；导航不再用模型摘要冒充本来源正文。保留既有排序、多路召回、关系扩展及主动检索职责。 |
| 语义能力与表示模型 | 增加可选、与供应商无关的来源片段编解码；对外提交结构不变，模型侧输出为片段引用。沿用默认 Qwen3.8 Flash，语义/Reader 为 xhigh、Judge 为 medium，未放宽工具与读取预算。 |
| 数据结构与存储 | 保留现有索引、原文和可重建状态。此次现场未证明存储布局是故障或主要时延根因，不作无依据替换。 |
| 执行架构与状态维护 | 保持既有隔离、恢复及并发边界。不同批量、编码和来源排布试验没有证明整体提速；不继续为时延目标试参数，也不据此声称已经没有优化空间。 |

验证结论：

- **三个故障均已通过**：`c055`、`c066`、`c076` 分别读取所需真实来源并答对；三题 38 份资产的摘要与事实线索均属于本来源，跨来源标识混入为 **0/38**，旧 V2 同题复测为 **34/38**。这是来源正确性指标，不是最终回答准确率。
- **22/22 正确**：3 个故障、2 个修正题、8 个既有风险回归、9 个另行构造案例。覆盖跨源连接、同名分组、更新、角色、中文、拒答、条件、表格、脚注、权威与时效、同源远距片段。新构造案例已保存为 `benchmarks/acceptance/suite/iteration/v2/source-ownership-regression.json`；使用后属于回归资产，不再充当全新盲测。
- **性能不冒充全面提升**：同一 11 题对照，两版均 11/11；工具调用总数 **77 → 60**（减少 22.1%），单题工具累计均值 **1218.55 → 1229.00 ms**，语义组织均值 **43.31 → 55.76 s**，整题均值 **86.81 → 111.04 s**。此次运行未证明整体提速，组织与模型执行成本仍是代价，不能用其他更容易的题稀释比较，也不能把全部差异归因于模型波动。
- **验证范围**：来源编解码 4 项、适配器 55 项、候选构建 6 项、迭代验证 29 项测试通过；Go 来源展示、候选辅助、语义与检索定向测试通过。旧 V2 中两个批量测试失败可原样复现，未冒充新回归修复或全仓测试通过。

两道题只修正问题表述，历史、答案、证据与其他 48 题不变；修正记录和完整数据路径见 `benchmarks/acceptance/suite/iteration/v2/stage6-question-corrections.json`。不把旧错题离线改判为通过。

本轮结束于缺陷修复和有证据的方向优化。该候选证明了来源可靠性提升，但**不是已经证明端到端全面更优的晋升版本**；未运行新候选完整四级关卡或正式 LongMemEval-S，不继承旧候选的关卡通过状态，也不宣称世界前沿水平。后续优化须有新的瓶颈证据，不继续无收益的参数试验。

结果入口：[22 题报告](E:/Ownward/acceptance/longmemeval-s/runs/source-ownership/owned-passages-validation/report.json)、[同题旧 V2 对照](E:/Ownward/acceptance/longmemeval-s/runs/source-ownership/old-v2-comparison/report.json)；原始工作、模型输出与实际工具调用均在对应 `questions/` 中。

## 质量优先裁决（2026-09-05）

结论：保留来源绑定修复作为后续候选，不因尚未提速退回已知错误的组织方式；本轮不继续改内核、换模型或重复跑题。新版是否通过完整关卡及最终测试仍未证明。

同一 11 题、相同环境与模型档位的已有逐题记录分解如下；不是新增运行，旧结果未改写：

| 测量对象 | 旧 V2 | 修复候选 |
| --- | ---: | ---: |
| 单题语义组织均值 | 43.31 s | 55.76 s |
| 单题主动检索工具累计均值 | 1.219 s | 1.229 s |
| 主动搜索、读取至回答完成均值（含外部推理及工具） | 34.42 s | 37.88 s |
| 单次工具调用 P95 | 703 ms | 703 ms |
| 裁判计分均值 | 3.09 s | 10.07 s |
| 建库至计分的评测整题均值 | 86.81 s | 111.04 s |

新增约 24.24 s 中，语义组织占 12.45 s，裁判占 6.98 s；工具累计只增加 0.010 s。整题包含重新建库、语义组织、回答和裁判，不等于用户对已组织信息的一次检索耗时；裁判是评测成本，不是内核检索成本。语义请求数、资产数均未增加；不同题目的组织耗时有升有降，新运行存在一次结构重试和一次 Reader 传输重试，现有非交错样本不能把差异全部归因于修复或全部归因于环境。

22 题中，工具累计均值/P95/最大值为 0.705/2.702/3.219 s，主动搜索至回答均值/最大值为 25.37/69.81 s。当前证据未显示不可接受的检索开销，足以支持保留修复并继续关卡；语义组织成本真实存在，不能隐去，也不能由该样本宣称所有规模的产品时效或前沿水平已经达标。按质量优先原则，本轮不再用重复测量追求“必须同时更快”。

评测流程已独立提交为 `0ec7098`；本提交封存内核修复与本裁决。下一工作边界是冻结所选修复候选、复用题库按 5/15/25/50 顺序取得该候选的结果，不重复模型资格化，不提前运行完整内部验收或正式 LongMemEval-S。

## 复核入口

- [原题、完整历史与冻结答案](E:/Ownward/acceptance/longmemeval-s/runs/kvs/8e1500b3630384e4d4230d54ed74f509392fce64d29d88c65cfbbca113139980/accepted/dataset.json)：按上述题号定位。
- [本批失败现场](E:/Ownward/acceptance/longmemeval-s/runs/kvs/8e1500b3630384e4d4230d54ed74f509392fce64d29d88c65cfbbca113139980/candidate/questions)：每题 `result.json` 保存调用次序与答案；`checkpoint.json` 映射 session/asset；`semantic-traces/*/work.json`、`analysis.json` 及 `_analysis/*/unit-000/codex/complete.json` 对照输入、原始模型输出和提交结果。
- [冻结展示实现](E:/Dev/ownward/.tmp/kernel-v2-major-iteration/stage6/read-frontier-candidate-lossless-short-context/core-service.go.overlay:637)：文件 SHA-256 为 `60b42d9821005c6ae3779197de317b3e4005c46bca725fa0ce75c8603b156ece`，与候选登记一致。
- [语义输入契约](E:/Dev/ownward/benchmarks/longmemeval_s/semantic_representation.py:43)、[提交装配](E:/Dev/ownward/benchmarks/longmemeval_s/run.py:574)、[内核接收](E:/Dev/ownward/internal/semantics/work.go:247)、[失败参数留痕边界](E:/Dev/ownward/benchmarks/longmemeval_s/run.py:881)。
