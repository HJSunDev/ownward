# 模块生命周期验收

本验收只通过最终发布二进制的 CLI 与 MCP 契约触发存储、向量和语义生命周期，不调用内部实现，不以测试向量或人工状态替代正式路径。它验证派生世代切换与失败回退、向量能力不可用和空间隔离后的恢复，以及语义工作的版本、来源、证据、不确定性、冲突和过期边界。

```powershell
python benchmarks/acceptance/module_lifecycle/verify.py `
  --repository . `
  --binary bin/ownward.exe `
  --candidate <full-commit-sha> `
  --runtime-dir <accepted-product-runtime> `
  --evidence-dir <new-evidence-directory> `
  --output <module-lifecycle-report.json>
```
