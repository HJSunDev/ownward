---
name: git-commit-generator
description: Generate a commit message by analyzing staged and unstaged git changes. Use when the user asks for help writing a commit message, summarizing git diffs, or reviewing staged changes for commit text.
disable-model-invocation: true
argument-hint: "[extra requirements]"
---

# Git Commit 生成器

该 skill 用于按项目规范生成 commit 消息，适合通过 `/git-commit-generator` 手动触发。

如果用户提供了额外要求，先纳入约束再开始分析：

`$ARGUMENTS`

## 执行流程

1. 执行 `git status` 查看当前变更状态
2. 执行 `git diff --cached --stat` 获取已暂存变更的文件统计
3. 执行 `git diff --cached` 查看完整的已暂存变更内容，并逐文件阅读
4. 核对：分析的文件数量必须等于 `git diff --cached --stat` 显示的文件数量
5. 执行 `git diff` 查看未暂存的变更，作为补充上下文
6. 基于已暂存变更生成 commit 消息；未暂存内容只作为参考，不要误计入已提交范围

## 暂存区拆分规则

拆分标准：先枚举架构不变量，提交边界不得切开任何不变量；一次提交必须形成可独立理解、构建、测试、回滚的终态子集，可以缺能力但不得存在半升级语义或需要靠后续提交修复的中间债务。

- 在上述标准内优先按功能边界拆；属于同一不变量，或在同一类型、函数、迁移与测试中交织且拆开会形成半升级状态的改动必须合并。
- 代码与文档是否同提交，以能否共同形成真实终态判断：契约或行为说明随代码同步生效时合并，否则分开。
- 临时文件、运行态文件、协作看板默认不进正式提交，除非用户明确要求。
- 多个单元合并为一个提交时，在 commit body 按单元分段说明。

## 生成规则

格式：

```text
<type>(<scope>): <subject>

- 变更点1
- 变更点2
```

类型选择：

- `feat`: 新功能
- `fix`: 修复 bug
- `docs`: 文档
- `style`: 样式或格式整理
- `refactor`: 重构
- `perf`: 性能优化
- `test`: 测试
- `chore`: 构建、依赖或工具链

规则：

1. `subject` 使用英文祈使句
2. `subject` 首字母小写
3. `subject` 不加句号
4. `scope` 应尽可能具体，如 `chat/RecentChatList`
5. 详细描述部分使用中文，列出具体实现的功能点

## 输出要求

- 最终结果必须包裹在 ```text 代码块中，便于一键复制
- 不要输出 `git add`、`git commit` 等命令
- 不要执行 git 写操作
- 如果暂存区为空，明确说明当前没有可用于生成正式 commit 消息的已暂存变更

## 示例

```text
feat(user/profile): implement user avatar upload

- 实现用户头像图片的上传功能
- 添加对图片格式（jpg/png）和大小（<2MB）的校验
- 更新用户资料页面的头像显示逻辑
```

