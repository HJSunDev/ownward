# 可移植 SSP 编写规范

本参考文件是自包含的。即使当前环境没有 SSP 协议仓库和 validator 工具，也可以按它创建 SSP 包。

## 包身份

SSP 包是一个普通 Agent Skill 目录，只是在 `SKILL.md` 中增加 SSP metadata，并添加 step 文件。

必需 source 文件：

```text
package-name/
  SKILL.md
  steps/
    01-first-step.md
    02-second-step.md
```

可选文件：

```text
  references/
    supporting-file.md
  .ssp/
    manifest.json
```

包目录名必须与 `SKILL.md` frontmatter 中的 `name` 一致。`name` 同时是用户和 agent 可能直接触发的标识，应该短、稳定、好记，并能表达任务意图；优先 1-3 个英文小写连字符词。单看 `name` 应能大致知道用户想让 agent 做什么。不要只写对象名、项目名或领域名（例如 `xxx-core`），也不要把完整任务描述、所有阶段或项目路径塞进 `name`，这些信息应放在 `description` 或 step 文件中。

## `SKILL.md` Source 契约

`SKILL.md` 必须作为普通 Agent Skill 可用。

最小 frontmatter：

```yaml
---
name: package-name
description: 当用户需要完成一个明确的分阶段结果时使用。
metadata:
  stepped-skill.version: "0.1"
  stepped-skill.entry: "steps/01-first-step.md"
---
```

规则：

- `name` 使用小写字母、数字和单个连字符；优先短名，但必须表达任务意图，避免长句式目录名和纯对象名。
- `description` 描述用户问题，不描述协议机制。
- v0 包的 `metadata.stepped-skill.version` 是 `"0.1"`。
- `metadata.stepped-skill.entry` 是安全的本地 `steps/*.md` 路径。
- 可选 `metadata.stepped-skill.required-extensions` 是逗号分隔字符串。
- 正文必须包含极简 entry pointer，说明高保真路径从哪个 entry step 开始。
- 正文不得包含 `Fallback Workflow`。`SKILL.md` 是入口，不是完整执行手册。
- `SKILL.md` 只能声明 entry step 路径，不得写完整 step loop、SSP step 数量、未来 `Next` 链，不得内联 entry step 或任何其他 step 正文。

## Step Source 契约

每个 step 文件应包含这些操作章节：

```markdown
## Objective
## Resources
## Instructions
## Output
## Completion Criteria
## Handoff
## Next
```

规则：

- `Resources` 必须是精确的 `None`，或 Markdown bullet list，列表项是 skill-root 相对文件路径。
- `Resources` 只表示打包在 Skill 目录内的支持文件。用户工作区、项目仓库、任务输入文件或被审查目标文件不属于 SSP `Resources`；如果 step 需要读取这些文件，把路径写在 `Instructions` 中作为任务输入。
- `Next` 只能有一个裸 target，或一个 code span target。
- `Next` 要么是 `END`，要么是安全的本地 `steps/*.md` 路径。
- 非终止 step 必须包含有用 handoff state。
- 终止 step 使用 `Next` = `END`。
- step 的 `Instructions` / `Handoff` 必须让当前 step 自带执行规则：完成当前 step，准备当前 output / handoff，然后读取 `Next`。
- 不要使用绝对路径、URL、query string、fragment、反斜杠、`..` 或 `.ssp/` resource。

## Manifest 投影规则

面向发布的包，应从 source 文件创建 `.ssp/manifest.json`。它是从 `SKILL.md` metadata、各 step 的 `Resources` 和 `Next` 推导出的生成物。

Manifest 形态：

```json
{
  "protocol": "stepped-skill",
  "version": "0.1",
  "entry": "steps/01-first-step.md",
  "steps": [
    {
      "id": "01-first-step",
      "path": "steps/01-first-step.md",
      "next": "steps/02-second-step.md",
      "resources": []
    },
    {
      "id": "02-second-step",
      "path": "steps/02-second-step.md",
      "next": "END",
      "resources": []
    }
  ]
}
```

投影规则：

1. 从 `SKILL.md` 读取 `metadata.stepped-skill.version` 和 `metadata.stepped-skill.entry`。
2. 从 entry step 开始。
3. 对每个 step，读取它的 `Resources` 列表和 `Next` target。
4. 按执行顺序追加 manifest step object。
5. `id` 由 step path 生成：去掉 `steps/`，去掉 `.md`，把 `/` 替换为 `.`。
6. 持续到 `Next` 为 `END`。
7. 如果链路成环、指向缺失 step，或 publication 包包含不可达 step，验证失败。
8. 如果存在 `metadata.stepped-skill.required-extensions`，将它按逗号分隔并 trim 后投影为 `requiredExtensions` array。

任何 source 修改后，都要更新 manifest，或明确标记 manifest 已过期。

## 没有工具时的验证

没有 validator 时，报告“仅完成手动 SSP 验证”，并检查：

- 普通 Agent Skill frontmatter 合法；
- `SKILL.md` 是最小入口，不包含 `Fallback Workflow`；
- entry path 存在；
- 每个 step 都有必需章节；
- 每个 `Resources` 条目都是本地、相对、包内、存在的文件；
- 每个 `Next` 只有一个 target，并解析到 step 或 `END`；
- 链路无环；
- publication package 不包含不可达 step 文件；
- 如果存在 manifest，它必须与投影链路一致；
- 包不声称 L0/L1 具备硬隔离或安全边界。
