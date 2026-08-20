# LongMemEval-V2

本目录把 Ownward 接入 LongMemEval-V2 官方评测器，正式验收由外部 Codex 通过 Ownward MCP 主动检索后向官方固定 Reader 提供证据；`direct` 只保留为可选诊断。Ownward 不接触标准答案、题型或评测函数。

评测固定使用 LongMemEval-V2 官方提交 `2cc8c540bdb87fe6761629b585e727e1c4704520`。Ownward 只使用轨迹文本和 29 张问题图片，不读取轨迹截图；数据准备只保留 `questions.jsonl`、Small haystack、`trajectories.jsonl` 和问题图片，使用官方 `--no-check-screenshots` 校验，不下载或解压 5.51 GiB 的轨迹截图归档。准备固定版本官方代码与 Python 环境后构建 Ownward：

```powershell
go build -trimpath -ldflags="-s -w -X main.version=<full-commit-sha>" -o bin/ownward.exe ./cmd/ownward
```

构建两个领域的共享状态时，必须使用候选发布默认的本地向量能力和[正式语义理解路径](../../docs/modules/semantics/README.md)。第一版由与主动检索相同的固定 Codex 运行条件兼任语义能力，通过 Ownward 统一契约完成内容理解；该角色只能看到当前语义工作及内核提供的资产上下文，不得接触问题、标准答案和评测函数。不得配置旧版 `OWNWARD_MODEL_*` 接口、额外模型服务或测试语义实现。查询无关且来源仍有效的语义结果可以在同一领域内复用，不得按每次轨迹引用重复推理。

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

固定提交中的 Small 共 451 个问题：`web` 240 个、`enterprise` 211 个。经数据和官方 Harness 核验，同一领域内的全部问题共享同一组有序的 100 条轨迹；官方 Harness 已按此语义每个领域只构建一次记忆，并支持在同一共享记忆上并发构建查询结果。因此，逐题重建、逐题复制状态或把 45,100 次轨迹引用当作创建次数都不是正式路径。

正式执行采用以下路径：

1. `web` 与 `enterprise` 分别从空白状态构建一次，完成组织和索引后冻结并保存；同一领域的全部问题只查询这一份状态。
2. 一个常驻 Ownward 进程唯一打开该领域的数据目录，通过仅绑定回环地址、使用本次随机凭证的 Streamable HTTP MCP 接入，为多个 Codex 会话提供并发只读查询。不得为每题启动一个 Ownward 进程，不得让多个进程同时打开同一数据目录，也不得复制状态换取并发。
3. Codex CLI `0.117.0` 通过 URL 和 Bearer Token 连接该服务；正式查询阶段只允许规则、检索、读取与关系导航，长期资产和派生状态均不得发生变化。并发前后的状态清单必须一致，每道题仍保留独立 Codex 会话、调用轨迹和结果。
4. 官方 `--prompt-build-max-workers` 使用预检固定的并发数；问题集、外部智能体、Reader、裁判、预算、计时和评分逻辑保持官方条件，不以并发改变评测口径。

全量前只做最低充分预检：先用一个代表性问题验证完整串行路径，再按机器资源和外部服务限额选定一个候选并发数执行一组小批量查询。只有出现错误或预计总时长仍不可接受时，才将并发减半或在资源明确有余量时提高一次，不遍历无价值的并发组合。固定并发必须同时满足：查询全部完成且 MCP 调用证据完整、无状态变化、无锁冲突和进程泄漏，单题查询时延仍具备通过公开前沿的余量，CPU、内存及外部调用限额均未越界。

预检以实际测量分别估算两个领域的一次构建、451 次主动查询、官方 Reader、裁判和归档耗时，并记录调用量、峰值资源、存储和外部费用。该方案消除重复构建和适配器人为串行等待；451 次相互独立的主动检索及官方 Reader、裁判仍是公开前沿证明不可删除的成本。若预估仍不可接受，只能继续修复执行路径，不得删题、缩小正式数据、放宽评分或用局部结果代替全量验收。
