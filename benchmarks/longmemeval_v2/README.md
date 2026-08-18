# LongMemEval-V2

本目录把 Ownward 接入 LongMemEval-V2 官方评测器，以同一内核验证两种使用方式：`direct` 直接返回检索证据，`active` 由外部 Codex 通过 Ownward MCP 主动检索后返回证据。两种方式均由官方固定 Reader 作答，Ownward 不接触标准答案、题型或评测函数。

评测固定使用 LongMemEval-V2 官方提交 `2cc8c540bdb87fe6761629b585e727e1c4704520`。先按官方仓库说明准备数据与 Python 环境，再构建 Ownward：

```powershell
go build -o bin/ownward.exe ./cmd/ownward
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
  --domain web `
  --questions-path <questions.json> `
  --haystack-path <haystack.json> `
  --trajectories-path <trajectories.json> `
  --memory-config-path benchmarks/longmemeval_v2/memory_config.direct.json `
  --output-dir <output> `
  --model <official-reader-model> `
  --prompt-build-max-workers 1
```

主动检索改用 `memory_config.active.json`，并通过 `--codex-binary` 指定 Codex CLI。适配器只有在 Codex 运行事件中存在成功的 Ownward MCP 调用时才接受结果。Ownward 当前数据目录采用进程级独占锁，因此官方评测的提示构建并发数固定为 `1`；这不会改变单次检索的计时口径，也避免并发进程争用同一份个人信息资产。

最终结果只以官方评测器生成的 `aggregated_metrics.json` 和完整运行材料为准；不得用本目录单元测试或局部样例替代正式结果。
