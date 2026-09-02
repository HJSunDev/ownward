# LongMemEval‑S 持久环境

本目录同时管理第一版社区基准的固定运行环境、冻结执行协议和正式适配器。正式资产安装一次后由候选验收只读复用；候选绑定、逐题检查点、报告、日志和临时产物只能写入环境根的 `runs/` 子目录。`environment.py` 负责安装与离线完整性，`protocol.json` 冻结正式口径，`run.py` 负责检查、执行和恢复；三者没有第二入口。

固定身份：

- 官方代码 `xiaowu0162/LongMemEval`：`9e0b455f4ef0e2ab8f2e582289761153549043fc`；
- 官方清洗数据 `xiaowu0162/longmemeval-cleaned`：`98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`；
- 官方轻量评测依赖：上述代码提交的 `requirements-lite.txt`；项目约束固定 `httpx==0.27.2`，兼容官方固定的 `openai==1.35.1`。安装后的完整解析版本写入环境清单并校验摘要。

正式协议固定每个问题使用独立 Ownward 数据目录；只把会话 ID、日期和用户—助手正文经公开创建与语义协作路径交给产品，不向记忆、组织和检索阶段暴露答案、答案会话、题型或裁判。语义工作仍按冻结身份、数量、顺序和 Schema 经公开路径提交。

产品评测固定为 `external-agent-progressive/v1`：问题经 `ownward.external-intelligence/v1` 稳定端口直接交给外部智能体；默认适配器使用 OpenCode Go `qwen3.8-flash` / `xhigh`，原 Codex Luna/Terra 组合仍可显式选择。Reader 获得 Ownward 的搜索、导航、证据检索、证据读取和完整读取工具，自主选择操作、积累证据、调整方向、判断停止并组织答案。宿主只执行工具和冻结预算，不预选来源、证据或读取顺序；每题最多 12 次工具调用、8 次读取和 24,000 字符已读证据。工具未暴露、被禁止、出现宿主固定预取或智能体未实际完成搜索与读取时，产品运行失败关闭。历史固定预取实现仅作为 `passive-ranking-diagnostic/v1` 内部诊断，不得进入盲测、正式验收、内核归因或晋升判断。

语义、主动检索回答与裁判共用有界的独立外部智能 worker；每个 worker 同时最多一个 turn，每次请求使用独立、临时、只读的新会话，单 worker 故障只重启自身且只能有界重试。默认 `opencode-server/v1` 使用 OpenCode Go / Qwen3.8 Flash；并列的 `codex-app-server/v1` 继续复用 Codex 原生认证和既有 Luna/Terra 能力。供应商与 driver 由 `benchmarks/support/external-intelligence-runtime.json` 封存，模型、推理档位与质量边界由角色配置和冻结协议共同封存；运行身份和检查点同时绑定所选实现、执行制品、工具清单与恢复策略，稳定端口不会隐藏来源。

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

正式 community 配置必须直接引用 `manifests/v1.json`、仓库内 `protocol.json`、当前外部智能适配器的执行制品与认证定位，并把候选运行目录置于 `runs/<candidate>/`；配置只保存定位和公开身份，不读取或复制认证内容。新配置使用 `external_intelligence` 块；既有 `codex_binary` / `codex_auth_file` 字段继续作为显式 Codex 兼容输入，不随默认值改变。不得引用 `.install`、系统临时目录或固定资产目录作为输出位置。500 题无模型 dry-plan 已封存并复用：23,867 个会话、1,498 个自然工作批，全部工作与正文完整。历史池 1/2/4 校准证据继续保留，但不再决定正式并发；活动规则是在候选池 8/12 中选择稳定且包含完整余量上界不超过 20,400 秒的最低并发。任何 provider 的正式运行都必须用自身身份重建 community binding 并通过四题 preflight，不能复用另一 provider 的模型检查点。
