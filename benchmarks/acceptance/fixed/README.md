# 固定回归执行

本目录以正式发布默认路径运行当前 `v5` 固定回归：候选发布包提供本地向量能力，同一个外部 Codex 经语义协作契约完成开放内容理解；语义阶段无法接触查询、答案和关系金标。固定回归只防止已知能力退化，不替代动态未见和公开前沿验收。

```powershell
python benchmarks/acceptance/fixed/verify.py `
  --repository . `
  --binary <release-ownward.exe> `
  --candidate <full-commit-sha> `
  --runtime-dir <accepted-product-runtime-directory> `
  --codex-binary <native-codex-executable> `
  --codex-auth-file <auth.json> `
  --evidence-dir <new-empty-evidence-directory> `
  --output <fixed-regression-report.json>
```
