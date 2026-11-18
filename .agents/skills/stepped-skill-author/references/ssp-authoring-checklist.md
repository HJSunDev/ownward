# SSP 编写检查表

创建或审查 Stepped Skill Protocol 包时使用本检查表。它是可移植的，不假设当前环境存在 SSP 协议仓库工具。

## 1. 适用性判断

以下条件全部成立时，适合使用 SSP：

- 工作天然分为多个阶段。
- 阶段顺序稳定。
- 每个阶段都能产出明确交付物。
- 每个阶段不需要读取未来阶段指令也能完成。
- 用户原本会把这些阶段分多轮交给 agent。

以下任一条件成立时，应使用普通 Skill：

- 任务短到可以舒适地放进一个 Skill body。
- 任务本质是一条强耦合推理链。
- 后续指令是理解当前阶段的必要条件。
- 拆分只是形式上的段落切分。
- v0 需要分支、循环、动态路由或并行执行。
- 用户需要硬保密或安全隔离。

## 2. 包结构

推荐 source layout：

```text
my-stepped-skill/
  SKILL.md
  steps/
    01-first-phase.md
    02-second-phase.md
    03-final-phase.md
  references/
    optional-supporting-material.md
  .ssp/
    manifest.json
```

包目录名必须和 `SKILL.md` 的 `name` 字段一致。`name` 应短、稳定、方便触发，并能表达任务意图；优先 1-3 个英文小写连字符词，不要用长句式目录名，也不要只用对象名、项目名或领域名。

## 3. `SKILL.md` 要求

`SKILL.md` 必须首先是合法普通 Agent Skill：

- 使用 YAML frontmatter。
- 包含 `name` 和 `description`。
- `name` 使用小写字母、数字和连字符，并与目录名一致；优先短名，但单看名字应能知道用户想让 agent 做什么，不把完整任务描述塞进 `name`。
- `description` 描述用户问题，不要把主要触发文本浪费在协议机制上。
- 需要时保留普通 Agent Skills 字段，例如 `license`、`compatibility`、`metadata`、`allowed-tools`。

SSP 只在 `metadata` 中增加命名空间字段：

```yaml
metadata:
  stepped-skill.version: "0.1"
  stepped-skill.entry: "steps/01-first-phase.md"
```

`SKILL.md` 还必须包含：

- 极简 entry pointer；
- entry step 路径；
- 只声明 entry step 路径，不内联 entry step 或任何其他 step 正文；
- 不要包含 `Fallback Workflow`；
- 不要复制任何高保真 step 指令到 `SKILL.md`。
- 不要列出未来 step 的精确文档清单、资源路径列表或详细检查表；这些内容属于对应 step。
- 不要在 `SKILL.md` 写完整 step loop、SSP step 数量或未来 `Next` 链。

`SKILL.md` 是普通 Skill 入口，不是完整执行手册。真实执行内容属于 `steps/`。

## 4. Step 文件要求

每个 step 文件应包含以下章节：

```markdown
# 当前阶段标题

## Objective

## Resources

## Instructions

## Output

## Completion Criteria

## Handoff

## Next
```

章节规则：

- `Objective` 只描述当前阶段。
- `Resources` 必须是精确的 `None`，或本地 skill-root 相对路径 bullet list。
- `Resources` 只列 skill 包内支持文件；用户工作区、项目仓库、任务输入文件或被审查目标文件应写在 `Instructions` 中，不作为 `Resources`。
- `Instructions` 提供足以完成当前阶段的说明，不依赖未来 step。
- `Output` 命名当前 step 的具体交付物。
- `Completion Criteria` 明确什么时候当前 step 算完成。
- `Handoff` 说明下一步所需的最小状态，并提示完成当前 step 后再读取 `Next`。
- `Next` 只能是一个本地 step 路径或 `END`。

SSP v0 中，每个非终止 step 只能有一个 `Next`，整条链必须终止于 `END`。

## 5. Manifest 要求

Publication package 应包含 `.ssp/manifest.json`。

作者不应该把 manifest 当成独立 source 手动维护。它应从 `SKILL.md` metadata、每个 step 的 `Resources` 和 `Next` 推导出来。

有 SSP generator 时使用 generator；没有 generator 时，按 `ssp-portable-spec.md` 手动创建或检查 manifest。

## 6. 验证清单

发布前检查：

- `SKILL.md` 作为普通 Skill 可用。
- SSP entry 存在。
- 每个 `Next` 都能解析，或为 `END`。
- 没有环。
- 没有不可达 step。
- resource 路径是本地、相对、包内路径。
- step resource 是精确文件路径，不是目录、URL、绝对路径或路径穿越。
- 用户工作区 / 项目仓库中的目标文件没有被误列为 `Resources`。
- handoff 期望清晰。
- 包不声称 L0/L1 具备硬隔离。
- 如果存在 manifest，它必须与 source 文件一致。

## 7. 反模式

避免：

- 空壳 `SKILL.md`，只让 agent 去读 steps。
- 长句式 `name`，把完整任务标题当目录名。
- 只表达对象或领域、不表达任务意图的 `name`，例如把“熟悉项目”命名成 `project-core`。
- 把一个推理任务任意切碎。
- 在 `SKILL.md` 中添加 `Fallback Workflow`。
- 在 `SKILL.md` 中预告完整 step 链、未来阶段精确资料清单或高保真步骤细节。
- 在 `SKILL.md` 中解释完整 step loop、列出 SSP step 数量或未来 `Next` 链。
- 在 `SKILL.md` 中内联 entry step 或任何其他 step 正文。
- 把用户项目文件、仓库文件或任务目标文件列入 `Resources`。
- 在当前 step 大段预告未来 step。
- 用大量禁令替代良好结构。
- 在 L0/L1 中暗示“模型不能看到未来 step”。
- 手写会漂移的 manifest。
- step 名称或正文必须读未来 step 才能理解当前 step。
