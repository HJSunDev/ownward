# LongMemEval‑S 持久环境

本目录同时管理第一版社区基准的固定运行环境、冻结执行协议和正式适配器。正式资产安装一次后由候选验收只读复用；候选绑定、逐题检查点、报告、日志和临时产物只能写入环境根的 `runs/` 子目录。`environment.py` 负责安装与离线完整性，`protocol.json` 冻结正式口径，`run.py` 负责检查、执行和恢复；三者没有第二入口。

模拟智能体通过公共函数 `benchmarks/support/ownward_mcp.py::product_instructions(client)` 获取所连接产品的协作规则：优先使用 MCP 初始化下发的 `instructions`，未提供时调用正式 `ownward_rules` 工具。Reader 将规则原样交给供应商无关的 `base_instructions`，任务提示只描述题目、只读范围、输出和预算，不另写产品使用策略。规则原文随 `request.json` 留存并参与请求身份，获取规则不占用题目的取证预算。其他测试可复用同一函数与调用端口；规则随产品制品发布，使用新规则须运行包含该规则的制品。

Reader 每次工具调用结束即保存该次尝试的 `reader/codex/attempt-*/active-retrieval.json`，包括失败调用；超时或重试不会抹掉先前尝试的取证轨迹。逐题 `failure.json` 引用这些文件。批量组织采用语义阶段的操作限时，进入检索后恢复查询限时，复用 HTTP 连接也遵守当前限时。

固定身份：

- 官方代码 `xiaowu0162/LongMemEval`：`9e0b455f4ef0e2ab8f2e582289761153549043fc`；
- 官方清洗数据 `xiaowu0162/longmemeval-cleaned`：`98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`；
- 官方轻量评测依赖：上述代码提交的 `requirements-lite.txt`；项目约束固定 `httpx==0.27.2`，兼容官方固定的 `openai==1.35.1`。安装后的完整解析版本写入环境清单并校验摘要。

正式协议固定每个问题使用独立 Ownward 数据目录；只把会话 ID、日期和用户—助手正文经公开创建与语义协作路径交给产品，不向记忆、组织和检索阶段暴露答案、答案会话、题型或裁判。语义工作仍按冻结身份、数量、顺序和 Schema 经公开路径提交。

产品评测固定为 `external-agent-progressive/v1`：问题经 `ownward.external-intelligence/v1` 稳定端口直接交给外部智能体；默认轻量适配器使用 OpenCode Go `qwen3.8-flash`，语义/Judge 为 `medium`、Reader 为 `xhigh`，原 Codex Luna/Terra 组合仍可显式选择。Reader 获得 Ownward 的搜索、导航、证据检索、证据读取和完整读取工具，自主选择操作、积累证据、调整方向、判断停止并组织答案。宿主只执行工具和冻结预算，不预选来源、证据或读取顺序；每题最多 12 次工具调用、8 次读取和 24,000 字符已读证据。工具未暴露、被禁止、出现宿主固定预取或智能体未实际完成搜索与读取时，产品运行失败关闭。历史固定预取实现仅作为 `passive-ranking-diagnostic/v1` 内部诊断，不得进入盲测、正式验收、内核归因或晋升判断。

语义、主动检索回答与裁判共用有界的独立外部智能 worker；每个 worker 同时最多一个 turn，每次请求使用独立、临时、只读的新会话，单 worker 故障只重启自身且只能有界重试。默认 `opencode-go-api/v1`（保留历史兼容标识）在宿主内直接调用阿里百炼 Token Plan / Qwen3.8 Flash，使用独立请求上下文承载 8 路并发；并列的 `opencode-server/v1` 与 `codex-app-server/v1` 保留进程池实现和原有模型配置。供应商与 driver 由 `benchmarks/support/external-intelligence-runtime.json` 封存，模型、推理档位与质量边界由角色配置和冻结协议共同封存；运行身份和检查点同时绑定所选实现、执行制品、工具清单与恢复策略，稳定端口不会隐藏来源。

正式结果标识为 `Ownward LongMemEval-S Production Profile`。官方数据、500 题、问答协议、提示和计分语义保持不变；由于 Reader、裁判与检索预算不同的公开成绩不具备直接可比性，本口径不设置跨 profile 准确率硬阈值。产品答案先独立冻结，评测层随后才接触官方答案与证据标识；逐题诊断封存语义组织、search/read、Reader、裁判、Token、重试、限流和时延证据，并与产品执行和正式计分单向隔离。

时延报告分为三层：`kernel_call_latency` 是单次 Ownward 工具调用墙钟，评价内核；`active_retrieval_cumulative` 是单题所有 Ownward 工具调用之和，评价内核与外部智能策略组合；`question_wall` 是问题开始至最终计分答案的整题墙钟，评价产品体验。三者不得共用不同测量对象的门槛。

整轮耗时采用 `execution.full_wall_policy: report-only`：5 小时 40 分及校准外推只作为报告中的参考指标，不拒绝启动、不截断正在推进的整轮测试，也不改变准确率。报告仍如实记录是否超过参考时间；单题、单次调用的超时和重试保护继续生效。省略该字段的旧协议保留原有硬时限行为，关卡自身的预算不变。

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
