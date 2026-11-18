---
name: stepped-skill-author
description: 用中文编写或审查可移植的 Stepped Skill Protocol（SSP）包。适用于 agent 需要判断一个自然语言工作流是否适合 SSP、设计 step 边界、编写最小化 SKILL.md 入口、创建 SSP step 文件、按需创建或检查 .ssp/manifest.json，或在不依赖 SSP 协议仓库的情况下验证分步 Skill 包的场景。
---

# 分步 Skill 作者

使用本 Skill 在任意项目中创建或修复 Stepped Skill Protocol 包。SSP 包必须首先是一个普通 Agent Skill；SSP 只是在此基础上增加一条有限的本地 step 链，用于高保真分阶段执行。

创建或审查 SSP 包前，先阅读 `references/ssp-portable-spec.md` 和 `references/ssp-authoring-checklist.md`。起草新包时，同时使用 `references/ssp-package-template.md`。

## 核心判断

只有当工作天然分阶段，并且用户原本会把这些阶段一段一段交给 agent 时，才使用 SSP。

如果任务很短、强耦合、拆分边界任意、当前阶段依赖未来指令、v0 需要动态分支，或用户需要硬安全隔离，应建议使用普通 Skill，而不是 SSP。

## 编写流程

1. 判断 SSP 是否适合这个任务。
2. 定义 Skill 的用户价值、触发条件、输入和最终输出。
3. 先选短、好触发且表达用户意图的 `name`：优先 1-3 个英文小写连字符词，单看名字应能大致知道用户想让 agent 做什么；不要只写对象名、项目名或领域名（例如 `xxx-core`），也不要把完整任务描述塞进名字；目录名必须等于 `name`，详细触发语义放进 `description`。
4. 设计一条有限线性的 step 链，边界必须来自真实阶段。
5. 把 `SKILL.md` 写成最小化普通 Skill 入口：只保留身份、触发说明、`metadata.stepped-skill.*` 字段和极简 entry pointer。`SKILL.md` 只能声明 entry step 路径；不得写 `Fallback Workflow`、完整 step loop、SSP step 数量、未来 `Next` 链、任何 step 正文、高保真指令、精确文档清单或资源路径。
6. 在 `steps/` 下编写 step 文件，确保每个 step 足以独立完成当前阶段。
7. 面向发布的包，应根据 source 文件和便携投影规则创建或检查 `.ssp/manifest.json`。
8. 有本地 validator 时把它作为可选确认；没有 validator 时，按内置检查表手动验证。
9. 先修复结构错误，再润色表达。

## 质量标准

- `SKILL.md` 是普通 Skill 入口，不是完整执行手册；真实执行内容属于 `steps/`。
- `name` 短、稳定、方便用户触发，并能表达任务意图；不要用长句式目录名，也不要只用对象名 / 领域名。
- `SKILL.md` 不包含 `Fallback Workflow`、任何 step 正文，也不暴露完整 step loop 或 step 链的高保真内容；精确的阶段文档清单、资源路径和细节检查表应留在对应 step。
- `Resources` 只列 skill 包内支持文件；用户项目、仓库或工作区中的目标文件应写在 step `Instructions` 中，不写进 `Resources`。
- 每个 step 都有清晰目标、输出契约、handoff 契约和一个 `Next`。
- 每个 step 自带当前步执行规则：先完成当前 step，准备当前 step 的 output / handoff，再读取 `Next`。
- 聚焦来自当前 step 自足，而不是要求模型“不要看未来文件”。
- 协议不得声称 L0/L1 具备硬隔离或安全边界。
- 作者维护的文件保持简单：`SKILL.md`、step 文件和可选资源。生成索引应从 source 推导出来。

## 验证

不要假设当前环境存在外部仓库、validator 或项目专属工具链。如果用户环境里有官方或本地 SSP validator，可以使用它；如果没有 validator，就按 `references/ssp-authoring-checklist.md` 手动检查，并明确说明本次只做了手动验证。
