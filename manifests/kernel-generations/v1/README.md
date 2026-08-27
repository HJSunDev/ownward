# 信息内核世代目录 v1

本目录只负责把已经存在的完整信息内核映射为可校验版本，不负责候选准备、晋升、回退或 Acceptance 生命周期。

[`catalog.json`](catalog.json) 中每个世代把组织、表示、检索、派生存储、执行、索引、资源与退化行为绑定为一个不可拆分组件。世代身份只由稳定契约、具名内容摘要、非秘密配置和权威基座、产品规则、语义、向量四项直接依赖身份产生；文件路径和 `audit.source_git` 只用于开发期追溯，不参与身份。

当前映射：

- V0：`1952f4b8…7b77`，唯一正式 Acceptance 基线；
- V1：`5bf5e617…6b99`，已有 4 个内部检查点但未晋升；
- 活动装配继续使用 [`current-collaborative.json`](../../compositions/v1/current-collaborative.json) 中已封存的当前内核实现。其控制状态身份只表示活动运行选择，不会改变 V0/V1 的候选资格。

只读校验：

```powershell
go run ./cmd/ownward-kernel-version verify `
  --repository . `
  --catalog manifests/kernel-generations/v1/catalog.json `
  --baseline benchmarks/acceptance/migration/v1/frozen-baseline.json
```

校验同时复算历史源码内容、直接依赖、世代与目录身份，并核对冻结候选二进制、报告摘要、唯一正式基线和 V1 四个检查点。失败不会写产品、控制或 Acceptance 状态。
