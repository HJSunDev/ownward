# 权威持久化候选生命周期

`ownward-authority-lifecycle` 是只在 `ownward_migration` 构建图中存在的离线迁移入口，不进入 `cmd/ownward` 的正常请求路径。它以封存组合和候选内容生成不可变计划，用独立候选目录和追加式检查点完成：

1. `plan`：校验候选内容、稳定契约、直接依赖和集成报告；
2. `prepare`：短暂取得活动资产浅快照，释放产品锁后在隔离目录完成长复制；
3. `catch-up`：只追加基线后的版本变化；
4. `promote`：只接受已经由 `catch-up` 追平并封存的候选；最终短写入屏障只核对双方完整快照与控制状态、写短检查点并以唯一 `control-state/v1` CAS 选择目标组合，发现尾部即拒绝；
5. `status`：校验或恢复 CAS 两侧的耐久检查点；
6. `observe`：通过时验证可恢复备份并接受，失败时先在屏障外追平只读回退源，再在最终屏障内收回唯一写入权。

权威数据目录、候选目录和检查点目录必须互不包含；计划、观察报告和备份也不得位于或包含这三类状态目录。活动事实始终是权威基座的控制状态；检查点只恢复流程，不拥有第二个活动选择。旧实现处于观察期时只能作为回退源，正式写入必须经过与活动组合绑定的写权守卫。

示例调用：

```powershell
go run -tags ownward_migration ./cmd/ownward-authority-lifecycle plan --repository <candidate-root> --baseline <sealed-baseline.json> --candidate <candidate.json> --integration <integration.json> --output <absolute-plan.json>
go run -tags ownward_migration ./cmd/ownward-authority-lifecycle prepare --plan <absolute-plan.json> --journal <absolute-journal-dir> --data-dir <absolute-data-dir> --candidate-dir <absolute-candidate-dir>
go run -tags ownward_migration ./cmd/ownward-authority-lifecycle catch-up --plan <absolute-plan.json> --journal <absolute-journal-dir> --data-dir <absolute-data-dir> --candidate-dir <absolute-candidate-dir>
go run -tags ownward_migration ./cmd/ownward-authority-lifecycle promote --plan <absolute-plan.json> --journal <absolute-journal-dir> --data-dir <absolute-data-dir> --candidate-dir <absolute-candidate-dir>
go run -tags ownward_migration ./cmd/ownward-authority-lifecycle status --plan <absolute-plan.json> --journal <absolute-journal-dir> --data-dir <absolute-data-dir> --candidate-dir <absolute-candidate-dir>
go run -tags ownward_migration ./cmd/ownward-authority-lifecycle observe --plan <absolute-plan.json> --journal <absolute-journal-dir> --data-dir <absolute-data-dir> --candidate-dir <absolute-candidate-dir> --observation <observation.json> --backup <absolute-backup-path>
```

观察失败不需要 `--backup`。候选接受后重复命令只读取并核对终态，不会再次复制、切换或写入检查点。
