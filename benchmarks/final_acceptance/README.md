# 最终验收汇总

最终完成判定只接受同一提交构建的发布二进制，以及由该二进制生成并全部通过的产品、性能、资源前沿、外部智能体和 LongMemEval-V2 正式报告。候选提交必须以 `-X main.version=<full-commit-sha>` 写入发布二进制；手填候选名称不能替代二进制版本与摘要绑定。

在各项正式验收完成后执行：

```powershell
python benchmarks/final_acceptance/verify.py `
  --repository . `
  --binary bin/ownward.exe `
  --candidate <full-commit-sha> `
  --baseline benchmarks/acceptance/v5/baseline.json `
  --thresholds benchmarks/acceptance/v5/thresholds.json `
  --product-report <product-report.json> `
  --performance-report <performance-report.json> `
  --resource-report <resource-frontier-report.json> `
  --resource-comparator-report <total-agent-memory-report.json> `
  --agent-report <agent-integration-report.json> `
  --official-repo .tmp/LongMemEval-V2 `
  --official-python <LongMemEval-environment-python> `
  --longmemeval-overview <submission_overview.json> `
  --output <final-acceptance-report.json>
```

验收器要求资源报告绑定同机复现的公开同类系统、完整十万条 384 维工作负载及其原始报告，Ownward 的各项资源和内核效率均不得落后。LongMemEval-V2 Small 的正式 submission package 至少包含一个由外部 Codex 主动检索的 `active` operating point；该点必须同时达到固定质量与时效前沿，官方 LAFS 不得为负。验收器会使用固定版本的官方代码重新校验完整运行材料与汇总值，确认最终归档和目录逐文件一致，并验证各域运行绑定同一候选、发布二进制和当前适配器。外部智能体两次会话的工具事实也必须与报告一同存在且摘要一致。汇总前还会确认仓库 HEAD 与候选一致、工作区干净，并重新执行模块校验、格式、静态检查、全部测试、构建及 Linux/macOS 交叉构建。
