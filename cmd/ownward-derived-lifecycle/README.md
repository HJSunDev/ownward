# 派生状态能力离线生命周期

本命令只在迁移安全边界构建，不进入 `cmd/ownward` 或正常产品请求链路：

```powershell
go build -tags ownward_migration ./cmd/ownward-derived-lifecycle
```

正式顺序是 `plan → prepare → catch-up → promote → observe`；`status` 只读恢复耐久阶段。所有路径参数必须为绝对路径。

- `plan --baseline --role --validation --output`：以封存基线、集成报告和当前二进制内置 collaborative 组合生成不可变计划；不接受候选身份参数。
- `prepare --plan --journal --data-dir --vector-bundle --generation`：短暂锁定权威基座取得正文与版本快照，释放后在不可见世代执行长耗时构建。
- `catch-up --plan --journal --data-dir --vector-bundle`：只处理前后快照的新增或更新版本，并重封存完整派生身份。
- `promote --plan --journal --data-dir`：在权威写锁内完成最终快照核对及派生指针、控制状态切换。
- `observe --plan --journal --data-dir --observation [--vector-bundle]`：通过时封存观察期间的完整活动尾部；失败时先用上一实现的二进制和向量包追平回退世代，再在最终屏障切换。

命令不会初始化缺失的权威控制状态，不读取活动派生结果构建候选，也不修改 Acceptance state。候选、直接依赖、快照、世代或观察证据错绑时失败开放；重复执行只复用身份完全一致的耐久检查点。
