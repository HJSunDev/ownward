# Ownward Acceptance Suite v1

本目录是第一版验收体系的唯一入口。体系包含一个内核前沿优化环和三层正式证据：固定内核基线、固定 Ownward 专项数据集、固定版本的官方清洗 LongMemEval‑S。仓库旧验收草案 `v1`～`v5` 只保留历史回归价值；候选冻结后生成数据、测试专用产品路径和其他并列完成轨道均不属于本体系。

`community` 机器契约和 `longmemeval` 稳定执行层只实现官方清洗 LongMemEval‑S；旧 LongMemEval‑V2 已无活动入口。正式结果统一标识为 `Ownward LongMemEval-S Production Profile`；固定环境、批内正文去重表示、Luna 上下文边界、独立单 turn Codex App Server 池及当前成本门禁见 [`benchmarks/longmemeval_s/README.md`](../../longmemeval_s/README.md) 与 [`docs/tasks/longmemeval-s-community-benchmark.md`](../../../docs/tasks/longmemeval-s-community-benchmark.md)。并发 8 的代表预检、精确恢复和证据链已成立，包含全部安全余量的全量上界低于 20,400 秒硬上限，`community` 已具备正式运行条件。

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

内核大版本迭代使用独立于正式执行器的唯一非正式入口。版本化比较合同位于 `iteration/v2/comparison-contract.json`，小型聚合基线位于 `iteration/v2/v0-baseline-facts.json`；二者只依赖受版本控制的冻结基线、内核目录和组合清单，在干净检出中可独立校验。入口只按组件内容与直接依赖选择 V0、当前产品或独立候选，不把 Git、文档或当前正式 `state.json` 当作候选或政策身份。每种证据写入独立非正式工作区的原子计划与检查点；同一身份可恢复，变化只生成该 subject 的新证据路径，不能晋升或切换内核：

```powershell
python benchmarks/acceptance/suite/kernel_iteration_run.py `
  --output .tmp\kernel-v2-major-iteration `
  --subject v0 `
  --evidence-type identity-calibration `
  --resume
```

当前机器 v6/v3 state、binding、报告摘要和基线历史属于可变运行态，必须显式只读校准，不能进入永久比较政策。校准会调用正式 state 校验并逐份核对现存报告，输出前后 state 字节摘要；阶段 5 合法重绑只产生新的校准身份，不改变比较政策或不依赖该校准的既有非正式证据：

```powershell
python benchmarks/acceptance/suite/kernel_iteration_run.py `
  --output .tmp\kernel-v2-major-iteration `
  --runtime-state .tmp\first-kernel-baseline-v1\acceptance\state.json `
  --resume
```

未来 V2 subject 使用 `ownward.kernel-iteration-subject/v1` 清单；问题池、开发、回归和整体验证均由同一入口以 `development`、`regression`、`integrated` 三种非正式证据执行。材料、执行器、观察器和共享条件先封存为 `ownward.kernel-iteration-input/v1`，候选达到冻结绝对门后才在完全相同的材料与条件上顺序运行 V0；中断只恢复身份未变的原子检查点，失败反馈只陈述首个可证阶段缺口。入口不会成为正式 contract 模式或证据层，也不能写正式 state：

```powershell
python benchmarks/acceptance/suite/kernel_iteration_run.py `
  --output .tmp\kernel-v2-major-iteration `
  --prepare-materials <materials.json> `
  --execution-config <execution.json> `
  --evidence-type development `
  --write-input <input.json>

python benchmarks/acceptance/suite/kernel_iteration_run.py `
  --output .tmp\kernel-v2-major-iteration `
  --subject-manifest <v2-subject.json> `
  --execution-config <execution.json> `
  --input-manifest <input.json> `
  --formal-state <state.json> `
  --evidence-type development `
  --resume
```

一次性盲测的生成、独立先验准入、Production Profile 执行/评价、不可逆摘要和销毁也由该入口管理。终态只保存身份、覆盖、聚合指标、判断和成本；题面、真值、证据及逐题输出在质量拒绝、失败或完成后销毁。运行中的 gate 只在临时 scratch 保存恢复 seed，并以活动定位文件连接 plan；终态删除两者后，保留一份不含秘密与盲测内容的当前依赖定位收据。新进程仅凭 plan identity 复用时会只读重算执行配置、环境、二进制、向量制品、模型协议和运行态校准等全部直接依赖，完全一致才返回零模型、零产品执行结果。历史终态可审计读取不等于当前预算仍有效。

```powershell
python benchmarks/acceptance/suite/kernel_iteration_run.py `
  --output .tmp\kernel-v2-major-iteration `
  --blind-plan-identity <plan-sha256> `
  --resume
```

版本化合同为 `iteration/v2/validation-contract.json`，五题非候选校准预算为 `iteration/v2/blind-calibration-budget.json`：5/15/25/50 题正常路径分别冻结为 406/751/1097/1961 秒，失败路径为 320/492/665/1097 秒，包含 20% 波动、10% 有界重试和每级 60 秒恢复余量，正常路径总计 4215 秒。版本化预算可脱离运行现场作为历史事实读取；将其用于当前阶段判断时必须同时验证计划、结果、依赖定位收据及全部当前直接依赖。

V1 的 `materials/optimization/v1/` 及历史分析实现只保留审计与回归价值；旧 `run.py kernel-iteration`、`kernel-storage`、`kernel-execution` 会明确拒绝，不再构成第二条生产路径。

内核前沿观察器由同一候选源码构建；正式运行会拒绝提交身份不一致或由脏工作树构建的观察器：

```powershell
go build -trimpath -o <frontier-binary> ./cmd/ownward-frontier
```

## 候选绑定

复制 `execution.example.json` 并填写当前阶段需要的真实路径。配置通过 `enabled_scopes` 显式选择本次要预检、绑定和执行的范围：`frontier` 只需要观察器，`core` 只需要候选二进制及其相邻向量能力包，`product` 才增加发布包、生产规模报告和 Codex；`community` 增加持久环境清单、冻结协议、候选运行目录以及现有 Codex 程序与认证文件路径。未启用范围不得被探测、下载、校验或写入绑定。Ownward 专项集仍固定使用 `gpt-5.4-mini` / `xhigh`；LongMemEval‑S 的语义组织固定使用 Codex `gpt-5.6-luna` / `low`，Reader 固定使用 Codex `gpt-5.6-luna` / `xhigh`，并与 Stage 6 绑定同一冻结 Reader 选择身份；裁判固定使用 Codex `gpt-5.6-terra` / `medium`。三者只复用现有 Codex 原生认证，不要求或探测额外 API Key；隔离预检真实调用三个冻结模型，但子集结果不形成正式成绩。当前 Reader 身份变化使 community binding 与 preflight 待重建，成本迁移收据不能替代它们。最多使用 24 条搜索线索和 8 条完整读取证据，具体机器值只以 `benchmarks/longmemeval_s/protocol.json` 为准。配置文件不进入仓库，不得把认证内容写入配置。候选代码稳定后，由唯一入口只生成当前启用范围的环境、输入、工具和候选执行制品绑定，再初始化或重新绑定状态：

```powershell
python benchmarks/acceptance/suite/run.py bind --config <execution.json> --output <binding-directory>
python benchmarks/acceptance/suite/run.py init --binding <binding-directory>/binding.json --state <state.json>
python benchmarks/acceptance/suite/run.py rebind --binding <binding-directory>/binding.json --state <state.json>
```

`ownward.acceptance-binding/v6` 以内容和声明依赖记录产品、权威基座、内核效果、内核世代、语义、向量与空间、接入、组合、二进制、发布制品、环境、观察者和验收工具身份；Git 提交只保留在 `audit.source_git` 供取回和审计，不参与产品、scope 或证据身份。`frontier` 使用冻结的语义/向量夹具，只绑定内核效果、环境、材料、观察者和验收工具；生产语义模型、向量模型、向量空间及权威持久化均不进入该报告身份。`core` 才绑定权威基座、内核、语义/向量能力、空间与候选二进制，`product` / `community` 进一步绑定完整产品、接入、组合和发布制品。纯 binding、状态迁移、失效与恢复代码由独立证据生命周期制品和 state 完整性保护，不是原始 scope 报告的直接依赖；报告接收/复核语义与执行关系语义则分别以 `report-reception`、`relationship-execution` 直接绑定其真实消费者。`summarize` 额外绑定独立 `summary-generation` 内容身份，因此汇总生成变化只失效汇总，不能隐藏在生命周期文件中或连带抹掉三份来源报告。某个直接依赖变化只失效真实消费它的 scope、实际嵌入该结果的汇总及相关基线，不按执行顺序或全仓提交传播；新增 `community` 不使内部检查点失效。`report_binding` 只保存旧报告逐字校验所需的兼容字段，不能代替新的直接依赖身份。

唯一正式 state 使用 `ownward.acceptance-state/v3`，每个检查点及基线记录都封存报告摘要与直接依赖图产生的证据身份；迁移历史与未来 `promote` 共用同一基线构造和只读校验合同。第 1 项冻结起点到 v3 的一次性入口如下；它也能把早期 v5/v2 的生命周期过度绑定原子收敛到唯一 v6/v3 结构。不带 `--write` 只读演练，带 `--write` 先封存不可变 binding 世代、再原子替换唯一 state、最后发布 binding 指针，任一源身份不符均不写状态。重复执行只校验已有迁移收据；报告、原始证据、检查点集合和基线历史均不重写，也不创建平行 state：

```powershell
python benchmarks/acceptance/migration/v1/migrate_evidence_identities.py
python benchmarks/acceptance/migration/v1/migrate_evidence_identities.py --write
```

## 执行

先按真实影响范围取得最低充分层级；普通开发不运行完整专项集或官方基准：

```powershell
python benchmarks/acceptance/suite/run.py plan --impact <local|asset|retrieval|organization>
python benchmarks/acceptance/suite/run.py plan --stage <kernel-baseline|stable-candidate|final-candidate>
```

所有正式层级只由 `execute` 调用。它直接启动对应观察器或适配器，验证规范报告后原子写入检查点；不能用手工报告、手填耗时或独立评分命令绕过执行与成本控制。专项报告同时记录 Ownward 内核查询、外部语义协作、智能体查询和逐场景端到端耗时；只有冻结的内核查询前沿参与产品硬判定，其他耗时如实呈现并受阶段异常停止与总成本约束，不另造来源不明的产品门槛。专项工具清单把完整活动文件机械划分为原始执行与解析/评分派生两种职责，并分别持久化摘要；只有候选、输入、Codex 二进制与模型参数、冻结任务、资源报告以及原始执行摘要全部相同，才允许当前解析器离线重放不可变原始轨迹并留下凭据。旧版清单只能通过绑定精确源清单与当前原始执行摘要的一次性迁移证明进入该流程；不存在按文件路径放行的长期白名单。任一原始事件缺失或改变、当前解析结论不一致，或命令、隔离环境、重试/超时、MCP 工具范围与执行适配器发生变化，均拒绝重放。中断后使用同一命令加 `--resume`，只复用依赖未变且摘要仍有效的完整结果，并只补当前层缺失部分；资格集已经封存的八个逐场景结果由完整集直接复用，完整集只补其余十六个场景：

```powershell
python benchmarks/acceptance/suite/run.py execute --state <state.json> --config <execution.json> --checkpoint-mode <targeted|core|frontier|qualification|full|longmemeval> [--resume]
```

影响计划只给出普通开发的最低充分反馈；`local` 不启动 Suite，`asset` 运行 `core`，`retrieval` 与 `organization` 只运行对应阶段的 `targeted`。完整前沿、资格集和后续正式层只由显式阶段计划触发。将非空的 `targeted_stages` 写入配置后，`targeted` 只运行这些阶段；`frontier` 使用完整固定内核材料且不以历史 `targeted` 为开始资格。当前候选的 `core`、完整 `frontier` 和 `qualification` 均通过且报告、原始证据和绑定仍有效时，才能晋升有效内核基线：

```powershell
python benchmarks/acceptance/suite/run.py promote --state <state.json>
```

候选稳定后运行固定内核基线和完整专项集；它们通过且候选保持冻结，才运行官方清洗 LongMemEval‑S。community 不以不同 Reader、裁判或预算下的公开分数建立硬阈值，只在完整 Production Profile 内报告准确率并允许等价口径直接比较。逐题诊断在产品答案冻结后生成，封存组织、search/read、Reader、裁判和成本证据，但与当前题、后续题及正式评分完全隔离。最后汇总同一候选的三层证据：

```powershell
python benchmarks/acceptance/suite/run.py summarize --state <state.json> --output <acceptance-workspace>/reports/suite.json
```

状态文件是中断恢复、局部失效和证据复用的唯一权威。产品组件、环境、输入、观察者或报告生产工具变化时，重新生成绑定并使用 `rebind`；只有 scope 身份真实变化才失效其消费者。纯 binding/lifecycle/身份迁移维护只更新独立生命周期制品和迁移完整性，不使既有报告失效。开始资格会在执行、恢复和晋升前重新核验，但不自动成为高成本结果的直接内容身份；直接依赖未变且资格重新成立时，已有结果可以复用。专项资源报告同时封存当前 `product` scope、发布包、阈值、生产规模证据、测量工具与原始证据身份。LongMemEval‑S 的逐题检查点写入持久环境 `runs/<candidate>/`，Suite 工作区封存官方结果、submission 与摘要；两者由同一 community binding 绑定。内部层的成本上限、报告结构和禁止事项以 `contract.json` 为机器权威；正式入口直接以 `report_relationships.py` 定义开始资格、scope、单次选择和汇总选择，以 `state_relationships.py` 定义阶段计划、失效与恢复传播，不存在合并两类职责的运行兼容入口。
