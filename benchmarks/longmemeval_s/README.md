# LongMemEval‑S 持久环境

本目录同时管理第一版社区基准的固定运行环境、冻结执行协议和正式适配器。正式资产安装一次后由候选验收只读复用；候选绑定、逐题检查点、报告、日志和临时产物只能写入环境根的 `runs/` 子目录。`environment.py` 负责安装与离线完整性，`protocol.json` 冻结正式口径，`run.py` 负责检查、执行和恢复；三者没有第二入口。

固定身份：

- 官方代码 `xiaowu0162/LongMemEval`：`9e0b455f4ef0e2ab8f2e582289761153549043fc`；
- 官方清洗数据 `xiaowu0162/longmemeval-cleaned`：`98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`；
- 官方轻量评测依赖：上述代码提交的 `requirements-lite.txt`；项目约束固定 `httpx==0.27.2`，兼容官方固定的 `openai==1.35.1`。安装后的完整解析版本写入环境清单并校验摘要。

正式协议固定每个问题使用独立 Ownward 数据目录；只把会话 ID、日期和用户—助手正文经公开创建与语义协作路径交给产品，不向记忆、组织和检索阶段暴露答案、答案会话、题型或裁判。语义工作仍按冻结身份、数量、顺序和 Schema 经公开路径提交。

产品评测固定为 `external-agent-progressive/v1`：问题直接交给外部智能体；Codex `gpt-5.6-luna` / `xhigh` 获得 Ownward 的搜索、导航、证据检索、证据读取和完整读取工具，自主选择操作、积累证据、调整方向、判断停止并组织答案。宿主只执行工具和冻结预算，不预选来源、证据或读取顺序；每题最多 12 次工具调用、8 次读取和 24,000 字符已读证据。工具未暴露、被禁止、出现宿主固定预取或智能体未实际完成搜索与读取时，产品运行失败关闭。历史固定预取实现仅作为 `passive-ranking-diagnostic/v1` 内部诊断，不得进入盲测、正式验收、内核归因或晋升判断。

语义、主动检索回答与裁判共用八个彼此独立的 Codex App Server worker；每个 worker 同时最多一个 turn，每次请求使用独立、临时、只读的新 thread，单 worker 故障只重启自身且只能有界重试。裁判固定使用 Codex `gpt-5.6-terra` / `medium` 按官方原始评测提示输出原标签。全部能力复用 Codex 原生认证，不使用额外 API Key。完整身份与成本上限见 `protocol.json`。

正式结果标识为 `Ownward LongMemEval-S Production Profile`。官方数据、500 题、问答协议、提示和计分语义保持不变；由于 Reader、裁判与检索预算不同的公开成绩不具备直接可比性，本口径不设置跨 profile 准确率硬阈值。产品答案先独立冻结，评测层随后才接触官方答案与证据标识；逐题诊断封存语义组织、search/read、Reader、裁判、Token、重试、限流和时延证据，并与产品执行和正式计分单向隔离。

正式机器固定在 E 盘：

```powershell
python benchmarks/longmemeval_s/environment.py install `
  --root E:\Ownward\acceptance\longmemeval-s `
  --bootstrap-python C:\path\to\Python39\python.exe
```

`install` 仅在清单不存在时联网取得固定源码和数据；环境已经成立后，同一命令只进行本地复核，不会克隆、下载或安装。日常及 Acceptance Suite 接入前使用完全离线的检查入口：

```powershell
python benchmarks/longmemeval_s/environment.py check `
  --root E:\Ownward\acceptance\longmemeval-s `
  --smoke
```

正式 community 配置必须直接引用 `manifests/v1.json`、仓库内 `protocol.json`、Codex 程序和认证文件位置，并把候选运行目录置于 `runs/<candidate>/`；配置只保存路径和模型身份，不复制认证内容。不得引用 `.install`、系统临时目录或固定资产目录作为输出位置。500 题无模型 dry-plan 已封存并复用：23,867 个会话、1,498 个自然工作批，全部工作与正文完整。历史池 1/2/4 校准证据继续保留，但不再决定正式并发；活动规则是在候选池 8/12 中选择稳定且包含完整余量上界不超过 20,400 秒的最低并发。旧四题预检绑定 Luna/medium，只保留传输、批次和历史成本基线价值。当前 xhigh 成本迁移使用实测 8.447 秒 p95 全额新增法，并只扣除已封存、含重复误差的 V2 本地关键路径节省：原始投影 12,339.621 秒，完整余量上界 19,635.850 秒，低于 20,400 秒且保留 764.150 秒。该证明不是正式 preflight；当前候选仍须在最终交接前重建 community binding 并运行一次 xhigh 四题 preflight。dry-plan 与历史 medium 预检证据分别位于 `runs/dry-plan/99f5190-appserver-v2` 和 `runs/preflight/77600820-89e959e3ae7c92f1-production-profile`。
