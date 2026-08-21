# 动态未见验收

本目录实现候选冻结后的动态未见验收及组织结构增益对照。`protocol.json` 在候选版本中冻结生成规模、覆盖范围、模型角色、资源预算、增量补充顺序和统计阈值。冻结候选前必须先按正式批次大小验证每类一个最小批次，并从每类取得至少一个合格场景，确保预检与正式数据路径一致。预检只证明生成器与独立验证者能够稳定协作，不进入正式验收。

验收依次生成隐藏语义世界、生成自然语言表达、由独立模型验证表达与真值一致，再将同一份冻结资产和派生状态分别交给完整 Ownward 和同一二进制的无关系组织模式。两组使用相同模型、状态、查询、外部智能体和预算；基线只设置 `OWNWARD_DISABLE_RELATIONS=true`，加载时屏蔽关系组织状态、关系信号和关系导航，不重新生成其他语义状态。

正式运行会输出动态报告、组织消融报告及二者引用的原始证据。正式数据按类别分批独立验证，合格场景立即冻结，只继续验证未达到六个有效场景的类别；已经成立的类别不会重跑。拒绝只依据产品运行前的数据质量，产品在正式数据冻结前不会运行。任一有效数据不得因产品结果不利而放弃或重跑；修改候选、产品配置、协议或数据实现后，原报告全部失效。

智能体答案只有在其信息标识与答案事实能够从成功的 Ownward 工具结果中直接复核时才计为正确；仅调用过工具或答案碰巧正确均不通过。

```powershell
python benchmarks/acceptance/dynamic/preflight.py `
  --protocol benchmarks/acceptance/dynamic/protocol.json `
  --evidence-dir <new-empty-preflight-directory> `
  --output <preflight-report.json> `
  --codex-binary <native-codex-0.117.0-executable> `
  --codex-auth-file <auth.json>
```

预检报告绑定正式协议、数据实现、Codex 二进制和生成/验证模型；任一绑定变化后必须重新预检，产品候选变化但这些绑定未变时直接复用。预检通过后才允许整理终态提交、冻结候选并构建发布包。

```powershell
python benchmarks/acceptance/dynamic/verify.py `
  --repository . `
  --binary <release-ownward.exe> `
  --candidate <full-commit-sha> `
  --evidence-dir <new-empty-evidence-directory> `
  --dynamic-output <dynamic-report.json> `
  --ablation-output <organization-ablation-report.json> `
  --codex-binary <native-codex-0.117.0-executable> `
  --codex-auth-file <auth.json> `
  --dataset-preflight-report <preflight-report.json> `
  --runtime-dir <accepted-product-runtime-directory>
```

生成器、独立验证者和外部智能体使用隔离的 Codex 运行环境；Ownward 候选只按正式发布默认方式启动。信息沉淀阶段由固定的外部智能体兼任第一版语义能力，通过[正式语义理解路径](../../../docs/modules/semantics/README.md)提交带来源、依据和不确定性的候选判断，完整组与无关系组复用同一份已冻结且来源仍有效的非关系语义结果。执行器会清除旧版 OpenAI 兼容接口的模型与凭证环境变量，不允许测试配置替代正式本地向量能力或语义理解路径。中断后仅可对原候选、原协议、原二进制和原模型配置增加 `--resume`；任何绑定不一致都会拒绝续跑。

## 执行成本

当前规模是统计规则允许的最小完整规模：四类任务各执行六个有效场景，共 24 个正式场景和 120 条信息。每类预先生成八个候选场景，按三个一组验证；达到六个有效场景立即停止该类，最多两个场景作为固定备用容量。某类有效数加剩余备用数已不足六个时立即停止整条数据流程，不再验证其他备用场景。正式样本在任何产品运行前按生成顺序逐类固定，多余的有效或未验证备用样本只记录、不执行，不得依据产品结果置换。减少正式规模将无法在当前 95% 置信规则下证明每类任务达到冻结下界。

数据生成只执行固定的八个小批次；独立验证初始最多八个批次，只有出现数据拒绝时才验证对应类别的备用批次。中断后复用所有已密封的生成、验证和产品检查点，不重新执行已经成立的工作。数据完成后只进行一次信息沉淀，以及四组完整/无关系检索对照；无关系条件使用完整状态的逐文件一致副本，不重新生成资产、向量或非关系语义，两种条件并行执行。

执行边界来自工作本身：数据阶段禁止工具并受固定输出模式和十分钟单阶段上限约束；Ownward 自身操作按冻结的内部时延边界执行；外部语义协作每阶段最多十分钟并记录真实 P95 与总成本，不使用来源不明的绝对质量门槛；资产读取与关系导航任何单次操作五秒未完成即停止，完整组与无关系组的资产核验并行执行；外部智能体按每题 LongMemEval-V2 前沿时效和固定工具调用数约束；所有 Codex 进程连续十分钟没有活动即终止。停止只表示本次执行路径无效，不能替代正式验收；修复后必须从有效检查点继续直至完整通过。
