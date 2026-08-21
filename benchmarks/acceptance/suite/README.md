# Ownward Acceptance Suite v1

本目录是第一版验收体系的唯一入口。体系包含一个内核前沿优化环和三层正式证据：固定内核基线、固定 Ownward 专项数据集、固定版本 LongMemEval‑V2。`v1`～`v5` 只保留历史回归价值；候选冻结后生成数据、测试专用产品路径和其他并列完成轨道均不属于本体系。

## 建设自检

以下操作只验证材料、契约、评分器、适配器、执行入口与证据生命周期，不写正式检查点，不晋升基线，也不形成产品验收结论：

```powershell
python benchmarks/acceptance/suite/run.py check
python -m unittest discover -s benchmarks/acceptance/suite -p "test_*.py" -v
python benchmarks/acceptance/suite/run.py self-check --output <self-check-report.json>
python benchmarks/acceptance/suite/run.py preflight --repository <repository> --binary <candidate-binary> --runtime-dir <embedding-bundle> --codex-binary <codex-executable> --codex-auth-file <codex-auth-file> --isolation-dir <new-empty-directory-on-non-system-drive>
```

内核前沿观察器由同一候选源码构建；正式运行会拒绝提交身份不一致或由脏工作树构建的观察器：

```powershell
go build -trimpath -o <frontier-binary> ./cmd/ownward-frontier
```

## 候选绑定

复制 `execution.example.json` 并填写真实路径、Qwen3.5-9B Reader 服务地址、官方裁判凭证变量名、官方数据参数和预检确定的并发数。Ownward 专项集和 LongMemEval‑V2 主动检索统一使用 `gpt-5.4-mini` / `xhigh`；Reader、裁判模型及其官方生成参数也已经冻结，不得改写。配置文件不进入仓库，不得把认证内容写入配置。候选代码与发布二进制稳定后，由唯一入口生成环境、输入、工具和候选绑定清单，再初始化或重新绑定状态：

```powershell
python benchmarks/acceptance/suite/run.py bind --config <execution.json> --output <binding-directory>
python benchmarks/acceptance/suite/run.py init --binding <binding-directory>/binding.json --state <state.json>
python benchmarks/acceptance/suite/run.py rebind --binding <binding-directory>/binding.json --state <state.json>
```

绑定会验证候选仓库完全干净，发布二进制由该候选提交的干净源码构建，专项集与社区基准使用同一候选和向量运行时，并将固定执行口径、环境、固定输入和验收工具分别纳入摘要。定向观察的阶段选择不属于冻结输入，不会使有效基线失效。每次执行前会重新验证这些事实；任一绑定事实变化即拒绝复用。

## 执行

先按真实影响范围取得最低充分层级；普通开发不运行完整专项集或官方基准：

```powershell
python benchmarks/acceptance/suite/run.py plan --impact <local|asset|retrieval|organization|candidate>
```

所有正式层级只由 `execute` 调用。它直接启动对应观察器或适配器，验证规范报告后原子写入检查点；不能用手工报告、手填耗时或独立评分命令绕过执行与成本控制。专项报告同时记录 Ownward 内核查询、外部语义协作、智能体查询和逐场景端到端耗时；只有冻结的内核查询前沿参与产品硬判定，其他耗时如实呈现并受阶段异常停止与总成本约束，不另造来源不明的产品门槛。中断后使用同一命令加 `--resume`，只复用绑定未变且摘要仍有效的完整结果，并只补当前层缺失部分；资格集已经封存的八个逐场景结果由完整集直接复用，完整集只补其余十六个场景：

```powershell
python benchmarks/acceptance/suite/run.py execute --state <state.json> --config <execution.json> --checkpoint-mode <targeted|core|frontier|qualification|full|longmemeval> [--resume]
```

`plan` 同时给出必要层级与前沿观察的受影响阶段；`local` 表示只运行代码自身的最小相关测试，不启动验收体系。将非空的 `targeted_stages` 写入执行配置后，`targeted` 只运行这些阶段；`frontier` 使用完整固定内核材料。完整前沿结果无受保护退化且有实质改善后，只能进入资格集；资格集通过后才能晋升有效内核基线：

```powershell
python benchmarks/acceptance/suite/run.py promote --state <state.json>
```

候选稳定后运行固定内核基线和完整专项集；它们通过且候选保持冻结，才运行 LongMemEval‑V2。最后汇总同一候选的三层证据：

```powershell
python benchmarks/acceptance/suite/run.py summarize --state <state.json> --output <acceptance-workspace>/reports/suite.json
```

状态文件是中断恢复、局部失效和证据复用的唯一权威。候选、二进制、环境、输入或工具变化时，重新生成绑定并使用 `rebind`；只失效受影响层时使用 `invalidate`。明确失效的旧最终报告不能作为崩溃恢复结果复活，仍与冻结绑定一致的逐场景证据可以继续复用。每份完成报告以工作区相对路径和摘要绑定其全部原始证据；任一上层开始前、结果复用时和最终汇总时都会重新核验前置报告与原始证据，缺失或变化即停止。LongMemEval‑V2 运行和 submission 也必须位于同一工作区。失败结果作为诊断证据保留，但不能解锁更高层；超时会终止当前外部步骤的完整进程树，成功后删除可由冻结输入重建的运行目录。成本上限、前置关系、报告结构和禁止事项以 `contract.json` 为机器权威。
