---
name: publish-main-ff
description: 当用户要求把本地 develop 分支上的既有提交以 ff-only 方式合并到 main、推送 origin/main、再切回 develop 时使用。必须严格按固定 Git 流程执行，不得修改合并策略，不得自动解决异常。
---

# ff-only 发布 main

本技能只用于固定流程：`develop -> main -> origin/main -> develop`。

这是高影响 Git 操作。只有用户明确要求把当前工作分支发布 / 推送到 `main` 时才执行。

禁止执行：commit、stage、stash、reset、rebase、pull、fetch、force-push、解决冲突、修改合并方式。

## 停止规则

任一步检查或命令失败，或者观察到的状态不完全符合预期，立即停止并向用户报告事实。失败后不得继续执行后续步骤。

报告内容：

- 失败步骤和命令
- 相关输出
- 当前分支
- 已停止，未尝试回滚

## 预检查

1. 执行 `git status --short`。
2. 如果输出不为空，立即停止。不得带着未提交或已暂存变更跨分支操作。
3. 执行 `git branch`。
4. 执行 `git branch --show-current`。
5. 如果当前分支不是严格等于 `develop`，立即停止。

## 发布流程

按顺序执行下列命令。每一步成功后才允许继续下一步。

1. `git checkout main`
2. `git branch`
3. `git branch --show-current`
4. 如果当前分支不是严格等于 `main`，立即停止。
5. `git merge --ff-only develop`
6. `git push origin main`
7. `git checkout develop`
8. `git branch --show-current`
9. 如果最终分支不是严格等于 `develop`，报告“发布已完成，但切回工作分支检查失败”。

## 成功回复

回复保持简洁：

- 说明已将 `develop` fast-forward 合并到 `main`
- 说明已推送 `origin/main`
- 说明当前已回到 `develop`
- 只有关键命令输出有实际帮助时才引用
