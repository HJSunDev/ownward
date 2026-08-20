# 资源前沿验收

资源前沿使用公开发布的 `total-agent-memory 12.4.0` 作为同类复现对象。它与 Ownward 同为本地持久化、384 维混合检索、关系组织和 MCP 接入的信息系统，并公开提供长期记忆质量结果；比较固定在同机、十万条信息、十万组 384 维向量和近十万条关系下进行。

比较只测信息内核，不把双方可替换的模型运行时计入进程资源或检索内核时延。对标系统的数据构造不计时，但必须写入其正式 SQLite、FTS、关系和向量格式；十万组向量由固定数值直接构造，不调用向量模型。写入、可检索、语义检索、空载与满载资源均通过其官方源码测量。

## 完整交付资源

信息内核是 Ownward 自身的核心能力，必须排除可替换模型后独立对标；完整交付轨道只评价用户实际承担的产品成本，不能替代或稀释内核结论。最终候选还必须测量正式发布包及安装后的全部文件，并采集 Ownward 主进程和其启动的模型运行时等完整进程树在冷启动、稳定空闲和实际检索期间的 CPU、内存、时延与存储占用；模型文件、运行时、许可证、派生数据和索引均不得排除或转移到未计量路径。模型生成时效与资源使用稳定的代表性查询和文档样本测量，十万条完整产品规模使用已生成的生产格式向量与索引验证，不重复执行十万次模型推理。

存在等价的完整本地产品时，以同期公开可复现前沿为基线；不存在单一可比对象时，以信息内核前沿和[向量模型方案](../../docs/research/vector-model-selection.md)的实测结果分别作为组件基线，并在查看最终候选结果前根据组件基线、重复测量误差和最低必要集成成本固定完整产品阈值。最终候选必须独立通过内核前沿和完整交付资源判定，不计算综合分；隔离模型测试、单进程采样或安装文件清单均不能单独证明通过。

对标环境固定使用官方 `v12.4.0` 源码、Python UTF-8 模式和 `mcp[cli]==1.29.0`。PyPI 轮子遗漏正式 SQL 迁移，不能完成全新数据目录上的真实写入；官方源码标签包含这些运行文件。源码在 Windows 上读取 UTF-8 迁移文件时没有声明编码，必须启用 Python 官方 UTF-8 模式。MCP 1.29.0 是仍支持该版本服务端 API 的维护版本，MCP 2.0 已移除相关接口。报告会绑定源码提交与内容、解释器、标准库、隔离环境和全部包版本，不能用残缺安装或缺失 Python 运行时的虚假闭包比较。

```powershell
<isolated-python> -X utf8 benchmarks/resource_frontier/tam_benchmark.py `
  --source-root <total-agent-memory-v12.4.0-checkout> `
  --output <total-agent-memory-report.json>

python benchmarks/resource_frontier/verify.py `
  --binary bin/ownward.exe `
  --candidate <full-commit-sha> `
  --performance-report <performance-report.json> `
  --comparator-report <total-agent-memory-report.json> `
  --output <resource-frontier-report.json>
```

正式报告绑定对标系统版本、完整 Python 运行闭包、关键发布源码、固定工作负载、Ownward 候选二进制与性能报告。任一资源或内核效率指标落后于复现基线即不通过。

对标系统报告不绑定 Ownward 候选。官方源码、运行闭包、硬件、操作系统和工作负载均未变化时，可以复用已经完整验证的同机报告；任一绑定条件变化才重新运行。Ownward 性能报告和完整交付资源报告始终针对最终候选重新生成。
