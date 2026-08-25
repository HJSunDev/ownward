# LongMemEval‑S 持久环境

本目录只管理第一版社区基准的固定运行环境，不实现正式 community 适配，也不运行 500 题验收。正式资产安装一次后由候选验收工作区只读复用；候选绑定、报告、日志和临时产物只能写入环境根的 `runs/` 子目录。

固定身份：

- 官方代码 `xiaowu0162/LongMemEval`：`9e0b455f4ef0e2ab8f2e582289761153549043fc`；
- 官方清洗数据 `xiaowu0162/longmemeval-cleaned`：`98d7416c24c778c2fee6e6f3006e7a073259d48f` 的 `longmemeval_s_cleaned.json`；
- 官方轻量评测依赖：上述代码提交的 `requirements-lite.txt`；项目约束固定 `httpx==0.27.2`，兼容官方固定的 `openai==1.35.1`。安装后的完整解析版本写入环境清单并校验摘要。

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

正式 community 配置迁移后必须直接引用 `manifests/v1.json`，并把候选运行目录置于 `runs/<candidate>/`；不得引用 `.install`、系统临时目录、旧 LongMemEval‑V2 或固定资产目录作为输出位置。
