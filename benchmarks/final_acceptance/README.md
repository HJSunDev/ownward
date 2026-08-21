# 最终验收汇总

最终完成判定只接受同一提交构建的发布二进制，以及针对该二进制生成并全部通过的固定回归、动态未见、组织结构增益对照、模块生命周期、性能、信息内核资源前沿、完整交付资源、外部智能体和 LongMemEval-V2 正式报告。候选提交必须以 `-X main.version=<full-commit-sha>` 写入发布二进制；手填候选名称不能替代二进制版本与摘要绑定。

目标汇总接口：

```powershell
python benchmarks/final_acceptance/verify.py `
  --repository . `
  --binary bin/ownward.exe `
  --candidate <full-commit-sha> `
  --baseline benchmarks/acceptance/v5/baseline.json `
  --thresholds benchmarks/acceptance/v5/thresholds.json `
  --product-report <product-report.json> `
  --dynamic-report <dynamic-unseen-report.json> `
  --organization-ablation-report <organization-ablation-report.json> `
  --module-lifecycle-report <module-lifecycle-report.json> `
  --performance-report <performance-report.json> `
  --resource-report <resource-frontier-report.json> `
  --delivery-resource-report <delivery-resource-report.json> `
  --resource-comparator-report <total-agent-memory-report.json> `
  --agent-report <agent-integration-report.json> `
  --official-repo .tmp/LongMemEval-V2 `
  --official-python <LongMemEval-environment-python> `
  --longmemeval-overview <submission_overview.json> `
  --output <final-acceptance-report.json>
```

汇总器在实现并验证动态未见、组织结构增益和模块生命周期三类报告前不得判定第一版完成。固定回归中的既有精确关系标签和外部语义耗时只作诊断；动态报告以冻结的统一关系定义、独立歧义验证、关系质量硬门槛及真实语义耗时和成本承担正式证明。动态报告必须绑定候选、发布二进制、生成方法、覆盖分布、随机来源、隐藏真值摘要、裁判方法、正式发布默认运行路径、执行边界、统计判定规则和完整原始证据；组织结构增益报告必须证明两组运行来自同一候选发布二进制和同一冻结状态的逐文件一致副本，除关闭关系组织状态、关系信号和关系导航外配置完全一致，并绑定同一批动态数据、最小有效差异、等价界限、逐类输出、质量、时效、总成本和统计结论。两组查询应当成对并行执行，不得把对照串行重复成额外等待。任一项不一致或缺失均不接受。

动态未见、组织结构增益和公开前沿共同证明当前检索编排；只有形成可能改变完成结论的具体替代方案时，才附加最小定向对照证据。模块生命周期报告必须覆盖存储派生世代的生成、校验、切换、失败回退和回收，完整向量能力世代的一致性、空间隔离、不可用降级和恢复，以及语义工作的资产版本绑定、来源与依据、不确定性、冲突、过期处理、不可用降级和恢复重评估；所有结果必须来自最终发布二进制的真实生产路径，但不为故障覆盖重复无关的规模数据和智能推理。

信息内核资源报告必须绑定同机复现的公开同类系统、完整十万条 384 维工作负载及其原始报告，Ownward 的各项资源和内核效率均不得落后；十万组向量直接使用生产格式数值，不触发十万次模型推理。完整交付资源报告必须绑定同一候选正式发布包，覆盖安装后的全部文件、完整进程树、模型与运行时、实际资产及派生数据，并证明冷启动、空闲和工作状态的体积、CPU、内存、存储与时延全部通过预先冻结的判定；任何随包交付或由 Ownward 启动的组件都不得排除。两份报告必须独立通过，汇总器不得以综合评分或一条轨道的优势弥补另一条失败。对标系统的已验证报告仅在源码、运行闭包、硬件、系统或工作负载变化时重跑。

LongMemEval-V2 Small 的正式 submission package 至少包含一个由外部 Codex 主动检索的 `active` operating point；该点必须同时达到固定质量与时效前沿，官方 LAFS 不得为负。验收器会使用固定版本的官方代码重新校验完整运行材料与汇总值，确认最终归档和目录逐文件一致，并验证各域运行绑定同一候选、发布二进制和当前适配器。外部智能体两次会话的工具事实也必须与报告一同存在且摘要一致。汇总前还会确认仓库 HEAD 与候选一致、工作区干净，并重新执行模块校验、格式、静态检查、全部测试、构建及 Linux/macOS 交叉构建。
