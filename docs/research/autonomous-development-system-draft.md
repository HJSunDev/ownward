# 高自主开发协作体系（迭代草案）

## 静态区｜目标

> 本区内容明确后不再修改。

我们要以 Ownward 的完整构建过程为首次实践，探索并形成一套可复用的目标模式开发体系：对于适合采用这种模式的任务，先由用户与 AI 共同明确并冻结需求、规范、交付边界和完成条件，再在这些约束内给予 AI 尽可能大的自主权，由 AI 自主完成方案选择、实施、验证和修复，直至满足完成条件后停止。

这套体系应通过文档、模板、Skill、提示词或其他合适的载体，低成本复用于后续的新项目或新需求，使用户和 AI 能够快速建立可靠的目标约束并进入自主执行，而不必重新摸索整套方法。

> **方案表达规则**：动态区中的方案必须简明、清晰，只保留关键判断、职责和关系；能够简短说清的内容不得长篇展开。

## 用户提示词

```text
目标：持续审查并迭代 `docs/research/autonomous-development-system-draft.md` 动态区中的方案，直到形成能够完整实现静态区目标、可低成本复用且在当前条件下最优的目标模式开发体系。

首个动作，以及每次续跑或历史压缩后的首个动作：重新读取本文档最新版本。静态区是唯一目标依据，未经用户明确授权不得修改；只允许修改动态区。不得用历史结论代替对最新内容的判断。

持续执行“完整审查—归并问题—集中修正—受影响范围复审”：

1. 从静态区重新推导方案必须解决的问题，审查动态方案能否形成从适用性判断、目标约束建立、自主执行、证据验收到停止与复用的最小完整闭环。
2. 逐项判断方案是否必要、清晰、可执行和可迁移；删除重复、矛盾、空泛、项目特例、当前工具绑定、过度流程及无法证明必要性的复杂度。“最优”是完整实现目标前提下最简单、可靠且可复用的方案，不是功能最多。
3. 每轮先完成整体审查，再按根因归并问题并集中修正；修正后只复审受影响范围。自主比较并收敛最优方案，不停留在罗列选项。
4. 准备结束时，对同一份未修改方案执行一次不复用旧结论的冷启动审查；发现问题则继续修正，并重新执行本步骤。


只有静态目标无法确定，且不同选择会改变目标或体系边界时才暂停询问；其余方案判断自主收敛。本任务只迭代动态区中的方案，不创建正式资产，也不进入 Ownward 的产品开发。

完成条件：方案完整实现静态目标；职责与依赖清晰；自主权边界、执行闭环、验收停止机制和复用方式均可实际运行；不存在目标缺失、相互矛盾、无效复杂度或对 Ownward、当前模型及具体工具的不必要绑定；同一份未修改方案通过冷启动审查。

满足后回复“目标模式开发体系方案已定稿”，简要说明最终方案与判断依据，立即停止，不再追加视角或扩大范围。
```

---

## 动态区｜方案

### 一、最终交付物

建立一个可独立复制或安装的 `target-mode-kit`：

```text
target-mode-kit/
├─ README.md
└─ skills/
   ├─ target-contract/
   │  ├─ SKILL.md
   │  ├─ references/
   │  │  ├─ applicability.md
   │  │  ├─ authority-model.md
   │  │  ├─ contract-quality.md
   │  │  └─ user-guidelines.md
   │  └─ assets/
   │     ├─ target-mode.template.yaml
   │     ├─ requirements.template.md
   │     ├─ development-guidelines.template.md
   │     ├─ delivery-contract.template.md
   │     ├─ execution-state.template.md
   │     ├─ decision-log.template.md
   │     ├─ method-feedback.template.md
   │     └─ run-prompt.template.md
   └─ target-run/
      ├─ SKILL.md
      └─ references/
         ├─ execution-loop.md
         └─ acceptance-and-stop.md
```

两个 Skill 都采用普通 Agent Skill，不使用线性 SSP：目标契约阶段需要反复澄清和适用性分支，执行阶段需要动态的实施—验证—修复循环。

| 工具包文件 | 内容 |
| --- | --- |
| `README.md` | 安装方式、两个 Skill 的触发方式、一次完整使用示例和版本升级规则 |
| 两个 `SKILL.md` | 只写触发条件、职责、输入输出和执行入口；详细规则下沉到各自 `references/` |
| `applicability.md` | 适用、不适用和需要先澄清的判定条件 |
| `authority-model.md` | 各类文档的职责、优先关系、冻结与变更规则 |
| `contract-quality.md` | 需求、规范、范围、验收与结束条件达到可执行状态的检查标准 |
| `user-guidelines.md` | 可跨项目复用的用户代码偏好、质量要求和协作规则；项目规范只补充特例 |
| `execution-loop.md` | 状态恢复、计划、实施、验证、修复、证据登记和暂停规则 |
| `acceptance-and-stop.md` | 逐项验收、冷启动终审、完成判定与立即停止规则 |
| `assets/` | 可直接实例化的目标包模板；只提供字段骨架和维护规则，不预填项目答案 |

### 二、两个 Skill

| Skill | 输入 | 负责 | 输出 |
| --- | --- | --- | --- |
| `target-contract` | 用户原始需求、已有项目文档、用户通用规范 | 判断任务是否适用；采用或创建项目文档；协助明确需求、规范和交付契约；执行跨文档审查；冻结目标；生成运行提示词 | 一套可直接启动的项目目标包 |
| `target-run` | 已冻结的项目目标包 | 恢复任务状态；自主完成架构、实现、验证和修复；维护证据与重大决策；按完成条件终审并停止 | 完成交付物、验收证据和最终状态 |

`target-contract` 允许频繁向用户确认产品判断；`target-run` 只有需要改变冻结目标、作出无法由依据确定的产品决策或取得新增高风险授权时才询问用户。

### 三、每个项目生成的目标包

以下是新项目的默认布局；已有项目不强制移动或复制任何现有文档，由 `target-contract` 采用原文件并在清单中登记实际路径：

```text
<project>/
├─ .target-mode/
│  ├─ target-mode.yaml
│  ├─ delivery-contract.md
│  ├─ execution-state.md
│  ├─ decision-log.md
│  ├─ method-feedback.md
│  └─ run-prompt.md
├─ <requirements document>
├─ <development guidelines document>
└─ <architecture document>            # 可选；执行阶段按需产生
```

| 文件 | 必须包含的内容 | 使用方式 |
| --- | --- | --- |
| `target-mode.yaml` | 目标包版本、当前状态，以及需求、规范、交付契约、可选架构、执行状态、决策记录和提示词的实际路径 | 两个 Skill 每次启动先读取；解决不同项目文件名和目录不一致的问题 |
| 需求文档 | 产品定位、用户、问题、范围、能力、原则和质量目标；不写架构与技术方案 | 定义“做什么”，是产品判断的最高依据 |
| 规范文档 | 用户通用开发偏好与项目特有的质量、安全和工程规则 | 约束“必须怎样做”，不得扩大产品范围 |
| `delivery-contract.md` | 本次目标、第一版范围内/外、逐项验收条件、每项所需证据、结束条件和授权门禁 | 冻结后决定任务何时完成；任何变更都要重新确认并升版本 |
| `execution-state.md` | 当前阶段、整体进度、已完成、当前工作、剩余工作、阻断项和验收证据索引 | 可覆盖更新，是续跑和历史压缩后的唯一运行状态 |
| `decision-log.md` | 重大决策、依据、替代方案、影响；纠正和推翻只能追加新记录 | 只追加，不记录普通进度，不得替代当前状态 |
| `method-feedback.md` | 本项目暴露的体系缺口、验证事实和建议改法 | 只提供通用工具包的候选改进；未经跨项目验证不得自动写回工具包 |
| `run-prompt.md` | 目标、权威文件入口、执行循环、暂停条件、完成条件和最终停止回复 | 由 `target-contract` 生成，用户直接用于启动目标模式；不复制需求正文 |

`target-mode.yaml` 的最小结构：

```yaml
schema: 1
status: draft # draft | frozen | running | complete
contract_version: 1
documents:
  requirements: []
  guidelines: []
  delivery_contract: .target-mode/delivery-contract.md
  architecture: []
  execution_state: .target-mode/execution-state.md
  decision_log: .target-mode/decision-log.md
  method_feedback: .target-mode/method-feedback.md
  run_prompt: .target-mode/run-prompt.md
```

### 四、权威关系

需求文档决定产品，规范文档约束实现，交付契约把两者收敛为本次可验收目标；架构由前三者推导，不得反向增加需求。Skill 和提示词只规定协作流程，执行状态、决策记录与方法反馈只保存状态和证据，均不得修改目标含义。

### 五、实际使用

1. 在新项目中调用 `target-contract`。
2. Skill 判断适用性，采用已有文档或从模板创建缺失文档。
3. 用户与 AI 迭代需求、规范和交付契约；审查无歧义、无冲突且可验收后，将清单状态改为 `frozen`。
4. Skill 生成 `run-prompt.md`；用户用它启动目标模式。
5. `target-run` 每次从 `target-mode.yaml` 和 `execution-state.md` 恢复，持续开发并为每项验收条件登记证据。
6. 交付物不再变化后执行冷启动终审；全部条件通过，将状态改为 `complete` 并停止。
7. 项目结束后审查 `method-feedback.md`，只有被证明与项目无关且可迁移的改进才进入下一版工具包。

### 六、在 Ownward 中落地

首次落地不移动现有四份文档：用 `target-mode.yaml` 登记现有需求、架构、开发规范和决策记录，只补建 `delivery-contract.md`、`execution-state.md`、`method-feedback.md` 与 `run-prompt.md`。Ownward 跑通后，在一个不同需求中不修改两个 Skill 的源文件重新生成目标包；能够完成第二次生成与启动，才证明体系具备复用性。
