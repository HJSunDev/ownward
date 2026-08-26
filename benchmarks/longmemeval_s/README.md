# LongMemEval‑S 持久环境

本目录同时管理第一版社区基准的固定运行环境、冻结执行协议和正式适配器。正式资产安装一次后由候选验收只读复用；候选绑定、逐题检查点、报告、日志和临时产物只能写入环境根的 `runs/` 子目录。`environment.py` 负责安装与离线完整性，`protocol.json` 冻结正式口径，`run.py` 负责检查、执行和恢复；三者没有第二入口。

固定身份：

- 官方代码 `xiaowu0162/LongMemEval`：`9e0b455f4ef0e2ab8f2e582289761153549043fc`；
- 官方清洗数据 `xiaowu0162/longmemeval-cleaned`：`98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`；
- 官方轻量评测依赖：上述代码提交的 `requirements-lite.txt`；项目约束固定 `httpx==0.27.2`，兼容官方固定的 `openai==1.35.1`。安装后的完整解析版本写入环境清单并校验摘要。

正式协议固定每个问题使用独立 Ownward 数据目录；只把会话 ID、日期和用户—助手正文经公开 create/semantic/search/read 路径交给产品，不向记忆、组织和检索阶段暴露问题、答案、答案会话、题型或裁判。宿主从公开 `semantic_work` 原序取得并固化最多 20 项结构化工作；每个原始自然工作批独立分析，批内资产与候选正文各列一次，工作项只以稳定 ID、revision、上下文及关系元数据引用正文。真实越界只能在该自然批内确定性拆分，不得跨批合并、截断或丢项。模型输出完成身份、数量、顺序和 Schema 校验后，仍按原工作批顺序经公开 `semantic_submit_batch` 提交。语义、Reader 与裁判请求共用八个彼此独立的 Codex App Server worker；每个 worker 同时最多一个 turn，每次请求使用独立、临时、只读的新 thread，单 worker故障只重启自身且只能有界重试。检索先经公开 `ownward_search` 冻结来源顺序；短资产继续完整读取，长资产只经公开 `ownward_evidence_search` 冻结至多三条源绑定区间，再用 `ownward_evidence_read` 读取。预算装载按 `source_rank + evidence_depth` 逐层惰性遍历，同层优先较浅深度，在来源广度与高排名来源深度之间提供有界公平；单条证据不适合剩余预算时只跳过该项，读满后不探测不可能交付的低位来源。全部结果仍共同受每题 8 条证据和 24,000 字符上限约束。检索后的 Reader 固定使用 Codex `gpt-5.6-luna` / `medium`，裁判固定使用 Codex `gpt-5.6-terra` / `medium` 按官方原始评测提示输出原标签；三者复用现有 Codex 原生认证，不使用额外 API Key。完整值与成本上限见 `protocol.json`。

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

正式 community 配置必须直接引用 `manifests/v1.json`、仓库内 `protocol.json`、Codex 程序和认证文件位置，并把候选运行目录置于 `runs/<candidate>/`；配置只保存路径和模型身份，不复制认证内容。不得引用 `.install`、系统临时目录或固定资产目录作为输出位置。500 题无模型 dry-plan 已封存并复用：23,867 个会话、1,498 个自然工作批，全部工作与正文完整。历史池 1/2/4 校准证据继续保留，但不再决定正式并发；活动规则是在候选池 8/12 中选择稳定且包含完整余量上界不超过 20,400 秒的最低并发。并发 8 的唯一一次四类完整代表预检处理 187 个会话、12 个自然语义批和各 4 个 Reader/裁判调用：20/20 次调用首次完成，0 重试、0 限流、0 中断、0 transport 超时、0 worker 重启，12 批原序提交，20 个请求使用不同只读临时 thread，精确恢复不改报告或检查点。墙钟为 132.359 秒；修正后的全量投影为 12,775.987 秒，加入 20% 正常波动、10% 有界重试与 3,600 秒恢复余量后为 20,053.902 秒，低于 20,400 秒硬上限，因此冻结并发 8 且不再测试 12。dry-plan 与当前代表预检证据分别位于 `runs/dry-plan/99f5190-appserver-v2` 和 `runs/preflight/77600820-89e959e3ae7c92f1-production-profile`。
