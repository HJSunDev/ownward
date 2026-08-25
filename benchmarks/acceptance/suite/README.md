# Ownward Acceptance Suite v1

本目录是第一版验收体系的唯一入口。体系包含一个内核前沿优化环和三层正式证据：固定内核基线、固定 Ownward 专项数据集、固定版本的官方清洗 LongMemEval‑S。仓库旧验收草案 `v1`～`v5` 只保留历史回归价值；候选冻结后生成数据、测试专用产品路径和其他并列完成轨道均不属于本体系。

`community` 机器契约和 `longmemeval` 稳定执行层只实现官方清洗 LongMemEval‑S；旧 LongMemEval‑V2 已无活动入口。固定环境、协议、全局 Codex 并发 8、300,000 字符分析输入边界及 18,913 秒全量校准投影见 [`benchmarks/longmemeval_s/README.md`](../../longmemeval_s/README.md) 与 [`docs/tasks/longmemeval-s-community-benchmark.md`](../../../docs/tasks/longmemeval-s-community-benchmark.md)。

## Ownward 专项材料版本

当前正式 product 输入为 `materials/product/v2/`。v2 在逐字节保留 v1 历史材料的前提下，只修正 `s27-68857f46` 的自然语言问题，使早先事件与当时认识、此后实际采用的做法、后续结果证据分别对应三项既有真值；事实、节点、关系、期望与禁止身份、答案真值、能力类别及资格集均未改变。`materials/manifest.json` 同时封存 v1 与 v2，Suite 自检会检测任一版本漂移；正式 product scope 只绑定活动 v2，不复用 v1 的 product 结果。

## 建设自检

以下操作只验证材料、契约、评分器、适配器、执行入口与证据生命周期，不写正式检查点，不晋升基线，也不形成产品验收结论：

```powershell
python benchmarks/acceptance/suite/run.py check
python -m unittest discover -s benchmarks/acceptance/suite -p "test_*.py" -v
python benchmarks/acceptance/suite/run.py self-check --output <self-check-report.json>
python benchmarks/acceptance/suite/run.py preflight --config <execution.json> --isolation-dir <new-empty-directory-on-non-system-drive>
```

内核前沿观察器由同一候选源码构建；正式运行会拒绝提交身份不一致或由脏工作树构建的观察器：

```powershell
go build -trimpath -o <frontier-binary> ./cmd/ownward-frontier
```

## 候选绑定

复制 `execution.example.json` 并填写当前阶段需要的真实路径。配置通过 `enabled_scopes` 显式选择本次要预检、绑定和执行的范围：`frontier` 只需要观察器，`core` 只需要候选二进制及其相邻向量能力包，`product` 才增加发布包、生产规模报告和 Codex；`community` 增加持久环境清单、冻结协议、候选运行目录以及现有 Codex 程序与认证文件路径。未启用范围不得被探测、下载、校验或写入绑定。Ownward 专项集固定使用 `gpt-5.4-mini` / `xhigh`；LongMemEval‑S 的语义组织固定使用 Codex `gpt-5.4-mini` / `low`，Reader 固定使用 Codex `gpt-5.4` / `medium`，裁判是独立的官方 `gpt-4o-2024-08-06`。隔离预检只夹具化裁判，不夹具化 Codex，不验证或要求裁判凭证，也不形成正式成绩。最多使用 24 条搜索线索和 8 条完整读取证据，具体机器值只以 `benchmarks/longmemeval_s/protocol.json` 为准。配置文件不进入仓库，不得把认证内容写入配置。候选代码稳定后，由唯一入口只生成当前启用范围的环境、输入、工具和候选执行制品绑定，再初始化或重新绑定状态：

```powershell
python benchmarks/acceptance/suite/run.py bind --config <execution.json> --output <binding-directory>
python benchmarks/acceptance/suite/run.py init --binding <binding-directory>/binding.json --state <state.json>
python benchmarks/acceptance/suite/run.py rebind --binding <binding-directory>/binding.json --state <state.json>
```

候选提交是全部范围共享且不可拼接的身份。发布二进制只属于真实运行它的 `core`、`product` 和 `community`；`frontier` 单独绑定由同一提交构建的观察器制品，跨候选比较的环境身份只包含真实测量环境，不包含必然随候选变化的观察器摘要。以后为同一候选新增 `community` 绑定不会使内部检查点失效。某一 scope 的直接事实变化只失效消费它的结果、实际嵌入该结果的汇总及包含该报告的有效基线，不按执行顺序向后传播。定向阶段选择属于单次 `targeted` 检查点身份，不属于冻结材料、完整前沿结果或有效基线。

## 执行

先按真实影响范围取得最低充分层级；普通开发不运行完整专项集或官方基准：

```powershell
python benchmarks/acceptance/suite/run.py plan --impact <local|asset|retrieval|organization>
python benchmarks/acceptance/suite/run.py plan --stage <kernel-baseline|stable-candidate|final-candidate>
```

所有正式层级只由 `execute` 调用。它直接启动对应观察器或适配器，验证规范报告后原子写入检查点；不能用手工报告、手填耗时或独立评分命令绕过执行与成本控制。专项报告同时记录 Ownward 内核查询、外部语义协作、智能体查询和逐场景端到端耗时；只有冻结的内核查询前沿参与产品硬判定，其他耗时如实呈现并受阶段异常停止与总成本约束，不另造来源不明的产品门槛。中断后使用同一命令加 `--resume`，只复用绑定未变且摘要仍有效的完整结果，并只补当前层缺失部分；资格集已经封存的八个逐场景结果由完整集直接复用，完整集只补其余十六个场景：

```powershell
python benchmarks/acceptance/suite/run.py execute --state <state.json> --config <execution.json> --checkpoint-mode <targeted|core|frontier|qualification|full|longmemeval> [--resume]
```

影响计划只给出普通开发的最低充分反馈；`local` 不启动 Suite，`asset` 运行 `core`，`retrieval` 与 `organization` 只运行对应阶段的 `targeted`。完整前沿、资格集和后续正式层只由显式阶段计划触发。将非空的 `targeted_stages` 写入配置后，`targeted` 只运行这些阶段；`frontier` 使用完整固定内核材料且不以历史 `targeted` 为开始资格。当前候选的 `core`、完整 `frontier` 和 `qualification` 均通过且报告、原始证据和绑定仍有效时，才能晋升有效内核基线：

```powershell
python benchmarks/acceptance/suite/run.py promote --state <state.json>
```

候选稳定后运行固定内核基线和完整专项集；它们通过且候选保持冻结，才运行官方清洗 LongMemEval‑S。最后汇总同一候选的三层证据：

```powershell
python benchmarks/acceptance/suite/run.py summarize --state <state.json> --output <acceptance-workspace>/reports/suite.json
```

状态文件是中断恢复、局部失效和证据复用的唯一权威。候选制品、环境、输入或工具变化时，重新生成绑定并使用 `rebind`；只失效受影响结果时使用 `invalidate`。开始资格会在执行、恢复和晋升前重新核验，但不自动成为高成本结果的直接内容身份；直接事实未变且资格重新成立时，已有结果可以复用。专项资源报告同时封存当前 `product` scope、发布包、阈值、生产规模证据、测量工具与原始证据身份。LongMemEval‑S 的逐题检查点写入持久环境 `runs/<candidate>/`，Suite 工作区封存官方结果、submission 与摘要；两者由同一 community binding 绑定。内部层的成本上限、报告结构和禁止事项以 `contract.json` 为机器权威；最低反馈、阶段触发、开始资格、scope、单次选择、失效和聚合关系以 `relationships.py` 为唯一机器权威。
