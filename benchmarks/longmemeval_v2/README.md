# LongMemEval-V2

本目录把 Ownward 接入 LongMemEval-V2 官方评测器，以同一内核验证两种使用方式：`direct` 直接返回检索证据，`active` 由外部 Codex 通过 Ownward MCP 主动检索后返回证据。两种方式均由官方固定 Reader 作答，Ownward 不接触标准答案、题型或评测函数。

评测固定使用 LongMemEval-V2 官方提交 `2cc8c540bdb87fe6761629b585e727e1c4704520`。先按官方仓库说明准备数据与 Python 环境，再构建 Ownward：

```powershell
go build -trimpath -ldflags="-s -w -X main.version=<full-commit-sha>" -o bin/ownward.exe ./cmd/ownward
```

配置真实语义模型：

```powershell
$env:OWNWARD_MODEL_BASE_URL = "<OpenAI-compatible endpoint>"
$env:OWNWARD_MODEL_API_KEY = "<key>"
$env:OWNWARD_CHAT_MODEL = "<chat model>"
$env:OWNWARD_EMBEDDING_MODEL = "<embedding model>"
$env:OWNWARD_EMBEDDING_DIMENSIONS = "<dimensions>"
```

通过 `run.py` 传入官方评测器的原有参数。直接检索示例：

```powershell
python benchmarks/longmemeval_v2/run.py `
  --official-repo .tmp/LongMemEval-V2 `
  --ownward-binary bin/ownward.exe `
  --candidate <commit-sha> `
  --domain web `
  --questions-path <questions.json> `
  --haystack-path <haystack.json> `
  --trajectories-path <trajectories.json> `
  --memory-config-path benchmarks/longmemeval_v2/memory_config.direct.json `
  --output-dir <output> `
  --model <official-reader-model> `
  --prompt-build-max-workers 1
```

主动检索改用 `memory_config.active.json`，通过 `--codex-binary` 指定官方基线使用的 Codex CLI `0.117.0`，并通过 `--codex-auth-file` 提供登录缓存；模型与推理强度固定为 `gpt-5.4-mini` / `xhigh`。每次查询只在隔离 `CODEX_HOME` 中使用临时认证副本，不读取用户配置和项目规则，查询结束立即销毁。只有 Codex 运行事件中存在成功的 Ownward MCP 调用时才接受结果。Ownward 当前数据目录采用进程级独占锁，因此官方评测的提示构建并发数固定为 `1`；这不会改变单次检索的计时口径，也避免并发进程争用同一份个人信息资产。

正式验收必须分别完成 Small 的 `web` 与 `enterprise`，并按官方 leaderboard 工具为外部智能体主动检索构建 `active` operating point 及最终 submission package；`direct` 可以作为同一 Ownward 方法的另一个 operating point 一并验证。构建最终 package 时，代码文件使用本目录的 `ownward_memory.py`。最终结果只以官方 `submission_overview.json`、完整运行材料、与目录完全一致的最终归档和非负 LAFS 为准；不得用本目录单元测试或局部样例替代正式结果。包装器会把候选提交、发布二进制摘要、检索模式、官方版本和适配器摘要写入各域的 `run_args.json`，用于证明最终结果来自同一候选版本。
