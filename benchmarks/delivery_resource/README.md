# 完整交付资源验收实现

本目录验证用户实际承担的第一版完整产品成本。验收对象必须是由当前候选二进制、锁定的 EmbeddingGemma 能力包、Ownward 许可证和使用说明组成的完整发布目录。

测试只对少量代表性文本执行真实模型推理；十万条容量使用 `ownward-performance` 直接构造的 512 维生产格式派生状态，不执行十万次模型推理。进程资源统计覆盖 Ownward 及其启动的完整子进程树。

```powershell
python benchmarks/delivery_resource/verify.py `
  --package <complete-release-directory> `
  --candidate <full-commit-sha> `
  --production-storage-report <100k-512d-production-storage-report.json> `
  --workspace <empty-workspace> `
  --evidence-dir <evidence-directory> `
  --output <delivery-resource-report.json>
```
