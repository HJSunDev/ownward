# LongMemEval-V2

本目录把 Ownward 接入 LongMemEval-V2 官方评测器，正式验收由外部 Codex 通过 Ownward MCP 主动检索后向官方固定 Reader 提供证据；`direct` 只保留为可选诊断。Ownward 不接触标准答案、题型或评测函数。

评测固定使用 LongMemEval-V2 官方提交 `2cc8c540bdb87fe6761629b585e727e1c4704520`。Ownward 只使用轨迹文本和 29 张问题图片，不读取轨迹截图；数据准备只保留 `questions.jsonl`、Small haystack、`trajectories.jsonl` 和问题图片，使用官方 `--no-check-screenshots` 校验，不下载或解压 5.51 GiB 的轨迹截图归档。准备固定版本官方代码与 Python 环境后构建 Ownward：

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

通过 `run.py` 传入官方评测器的原有参数。正式主动检索示例：

```powershell
python benchmarks/longmemeval_v2/run.py `
  --official-repo .tmp/LongMemEval-V2 `
  --ownward-binary bin/ownward.exe `
  --codex-binary <native-codex-0.117.0-executable> `
  --codex-auth-file <auth.json> `
  --candidate <commit-sha> `
  --domain web `
  --questions-path <questions.json> `
  --haystack-path <haystack.json> `
  --trajectories-path <trajectories.json> `
  --memory-config-path benchmarks/longmemeval_v2/memory_config.active.json `
  --output-dir <output> `
  --model <official-reader-model> `
  --prompt-build-max-workers <preflight-fixed-worker-count>
```

主动检索通过 `--codex-binary` 指定官方基线使用的 Codex CLI `0.117.0`，并通过 `--codex-auth-file` 提供登录缓存；模型与推理强度固定为 `gpt-5.4-mini` / `xhigh`。每次查询只在隔离 `CODEX_HOME` 中使用临时认证副本，不读取用户配置和项目规则，查询结束立即销毁。只有 Codex 运行事件中存在成功的 Ownward MCP 调用时才接受结果。

正式验收只要求分别完成 Small 的 `web` 与 `enterprise`，并按官方 leaderboard 工具为外部智能体主动检索构建一个 `active` operating point 及最终 submission package。`direct` 仅用于出现具体诊断需要时的可选对照，不属于完成条件，不得默认重复全量运行。构建最终 package 时，代码文件使用本目录的 `ownward_memory.py`。最终结果只以官方 `submission_overview.json`、完整运行材料、与目录完全一致的最终归档和非负 LAFS 为准；不得用本目录单元测试或局部样例替代正式结果。包装器会把候选提交、发布二进制摘要、检索模式、官方版本和适配器摘要写入各域的 `run_args.json`，用于证明最终结果来自同一候选版本。

## 执行成本边界

Small 包含 451 个问题和每题 100 条轨迹。当前官方逐题隔离语义下共有 45,100 次轨迹引用；本数据的 200 条唯一轨迹至少展开为 5,295 份信息文档，若逐题重复创建则至少触发 1,170,518 次 `create`，尚未计入超长文本继续分块。该路径不得进入正式运行。

正式全量执行前，适配器必须在不接触问题答案的前提下复用同一候选、同一模型和同一内容产生的查询无关语义与向量结果，同时保持每道题只能检索其 haystack 中的轨迹；各题的运行资产仍须隔离。随后先完成一题的端到端预检，根据真实创建、主动检索、Reader 和裁判耗时推算 451 题的总耗时、调用量、存储和外部费用，并固定安全并发与异常停止条件。复用后仍不可接受时继续优化执行路径，不得启动全量任务。
