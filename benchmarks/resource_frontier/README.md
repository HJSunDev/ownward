# 资源前沿验收

资源前沿使用公开发布的 `total-agent-memory 12.4.0` 作为同类复现对象。它与 Ownward 同为本地持久化、384 维混合检索、关系组织和 MCP 接入的信息系统，并公开提供长期记忆质量结果；比较固定在同机、十万条信息、十万组 384 维向量和近十万条关系下进行。

比较只测信息内核，不把双方可替换的模型运行时计入进程资源或检索内核时延。对标系统的数据构造不计时，但必须写入其正式 SQLite、FTS、关系和向量格式；写入、可检索、语义检索、空载与满载资源均通过其官方源码测量。

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
