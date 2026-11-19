# 架构与工程待办

以下事项记录当前架构与工程实现的主要短板，不阻塞当前主线。

- [x] **清退已退役的非生产双轨遗留代码与失效工具。** 现役生产已收敛至 `core.StreamingAssets` 与 `boundedstore` 有界 SQLite 主链，但仓库仍保留生产不可达的旧内存核心（`core.Service`）、旧文件日志与派生索引（`assetlog`、`derived`）及绑定 SQLite 前旧目录格式的生命周期工具（`capabilitylifecycle`、`cmd/ownward-*-lifecycle`）。梳理仅用于历史一次性导入或公共值结构的最小复用部分，清退死代码与失效工具，同步收紧组合封存清单与关联模块说明。[现状边界](../architecture/overview.md#当前替换与研发边界)

- [ ] **打通现役 SQLite 跨组合升级闭环并收紧封存兼容边界。** 当前发布将窗口静态资源、宿主与接入组件、内核、存储及目标平台向量运行制品一并算入库级活动组合身份，但尚未实现现役 `ownward.sqlite` 的跨组合迁移能力，且 `ownward rebuild` 不支持并发变更追平切换，导致修改受封存前端或接入代码重打正式包会阻断存量库直接打开，跨操作系统实例也无法直接做计划性交接。拆分组件身份、平台运行制品与数据兼容边界，按[架构变更约束](../engineering/architecture-change-rules.md#能力替换)补齐现役库的跨组合迁移与追平切换机制。

- [ ] **补齐非 Windows 平台工程保护与多平台真机交付验证。** 核心存储与连接器虽可跨平台编译，但系统服务注册（`service-install`）、随包 Codex 插件交付、内置后台组织执行器的子进程资源约束（Job Object）与本机凭据系统级保护（DPAPI）目前仅在 Windows 完整实现；macOS 与 Linux 仍采用文件权限保护凭据、向量子进程缺少内核级父进程退出联动且不启用内置后台组织执行器。按目标平台补齐系统服务托管、凭据保护与受管子进程约束，完成跨平台真机安装运行及首版完整交付验收。[运行与平台现状](../architecture/overview.md#运行与部署)

- [ ] **精简有界 SQLite 派生存储开销并重校完整交付资源容量基线。** `cmd/ownward-production-storage` 已从无索引平文件 `organization.binlog` 改接至现役 `boundedstore` 有界 SQLite 生产格式（全量向量入块并执行对称 `VACUUM` 消除迁移空闲页），但持久化关系/组织 B 树与两级紧凑向量筛选页（`vector_filter_pages`）存在约 `5.1 KB/条` 的页与索引结构开销；在沿用 `87 B` 极短合成样本时测得派生存储比约 `2.21`（在 `10 kB` 真实平均文档长度下约 `0.42`），超出 SQLite 化前按无索引编码体积冻结的 `1.35` 上限，会阻断专项层完整交付资源验收（`product_resource/verify.py`）。专项排查现役 SQLite 派生表、块头与二级索引的存储精简空间，并结合真实用户文档长度分布重校容量工作负载与[完整交付资源阈值](../../benchmarks/acceptance/suite/adapters/product_resource/thresholds.json)。[生产存储验收实现](../../internal/performance/production_storage.go)、[累计容量与启动成本评估](../modules/storage/ingestion-and-derived-data.md#累计容量与启动成本评估)
