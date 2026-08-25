# LongMemEval‑S 持久环境

本目录同时管理第一版社区基准的固定运行环境、冻结执行协议和正式适配器。正式资产安装一次后由候选验收只读复用；候选绑定、逐题检查点、报告、日志和临时产物只能写入环境根的 `runs/` 子目录。`environment.py` 负责安装与离线完整性，`protocol.json` 冻结正式口径，`run.py` 负责检查、执行和恢复；三者没有第二入口。

固定身份：

- 官方代码 `xiaowu0162/LongMemEval`：`9e0b455f4ef0e2ab8f2e582289761153549043fc`；
- 官方清洗数据 `xiaowu0162/longmemeval-cleaned`：`98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`；
- 官方轻量评测依赖：上述代码提交的 `requirements-lite.txt`；项目约束固定 `httpx==0.27.2`，兼容官方固定的 `openai==1.35.1`。安装后的完整解析版本写入环境清单并校验摘要。

正式协议固定每个问题使用独立 Ownward 数据目录；只把会话 ID、日期和用户—助手正文经公开 create/semantic/search/read 路径交给产品，不向记忆、组织和检索阶段暴露问题、答案、答案会话、题型或裁判。宿主从公开 `semantic_work` 原序取得并固化最多 20 项结构化工作；保持每项工作、候选、提示模板和输出 Schema 不变，在 300,000 字符输入边界内拆成独立分析单元，由全局最多 8 个 Codex `gpt-5.4-mini` / `low` 调用并发完成，校验齐全后仍按原二十项工作批顺序经公开 `semantic_submit_batch` 提交。检索后的 Reader 只能使用 Codex `gpt-5.4` / `medium`，且与语义分析共用同一全局调用上限。二者复用现有 Codex 认证，不使用额外模型服务或 OpenAI API。官方 `gpt-4o-2024-08-06` 裁判是独立外部评分器；隔离预检只用显式确定性裁判夹具验证官方提示、报告和生命周期，不形成正式成绩。最多读取 8 条证据，完整值与成本上限见 `protocol.json`。

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

正式 community 配置必须直接引用 `manifests/v1.json`、仓库内 `protocol.json`、Codex 程序和认证文件位置，并把候选运行目录置于 `runs/<candidate>/`；配置只保存路径和模型身份，不复制认证内容。不得引用 `.install`、系统临时目录或固定资产目录作为输出位置。最终隔离校准使用四种题型的四个完整官方问题，共 187 个会话、12 个三批次语义工作批、31 个语义分析调用和 4 个 Reader 调用；全局并发实际达到 8，墙钟 165.141 秒，发生 2 次有界格式重取、0 限流、0 中断，12 批全部原序提交。全量投影为 18,912.927 秒，原每题串行分析投影为 34,295.760 秒；并发 8 已是最低且稳定并形成显著收益的校准点，因此未扩大到 12。证据位于 `E:\Ownward\acceptance\longmemeval-s\runs\preflight\ffaeb2f5-70f19288788f4196`，再次 `--resume` 后全部证据摘要保持不变。稳定层名 `longmemeval` 执行完整 500 题；中断后 `--resume` 只补未完成问题，绑定变化由 Suite 局部失效 community。
