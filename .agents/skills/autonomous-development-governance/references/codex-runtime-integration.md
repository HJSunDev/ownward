# Codex 运行集成与冷启动验收

本文记录自主治理运行时在 Codex 本地项目中的已验证集成故障、适用边界和冷启动验收要求。它不说明 Ownward 产品行为，也不能泛化为所有 Codex、操作系统或 MCP 环境都会发生同一问题。

## 适用环境

2026-08-22 的故障与修复发生在以下组合环境：

- Windows 上的 Codex Desktop 项目任务，工作区绑定 `E:\Dev\ownward`；同机 Codex CLI 用于建立全新会话执行冷启动验证；
- Codex 上层本机配置定义了 Ownward stdio MCP 的完整 `command`、`args`、`cwd` 并默认禁用；项目 `.codex/config.toml` 只把同名服务设为 `enabled = true`、`required = true`，合并后使用仓库内二进制和固定 `.ownward/development` 数据目录；
- Governor 是只读自定义子 Agent，不需要调用 Ownward 产品 MCP；
- 项目使用 `.codex/hooks.json` 中的非托管 Hooks，需要 Codex 对当前定义的精确哈希完成信任；
- 本机 Codex CLI 最初为 `0.130.0`，排查时更新为 `0.149.0`。

没有项目级 MCP、MCP 可安全共享或并行打开同一状态、Governor 本来就需要该 MCP、Hooks 由平台托管，或自定义 Agent 配置加载规则不同的环境，不应直接套用本次故障结论。

## 问题背景

此前 Codex 任务绑定在另一个项目，只能通过绝对路径操作 Ownward，不能在真实项目归属下加载和验证 Ownward 的项目 Hooks。任务迁回 Ownward 后，先完成了治理脚本的直接调用和最小父子 Agent 通道测试；这些测试证明确定性脚本与原生返回通道可运行，却没有覆盖新任务冷启动时的 Hook 信任、项目配置合并和自定义 Agent 独立配置校验。

随后使用持续开发提示词正式启动新任务。Codex 自动进入 Governor 复核，但界面长期停留在 MCP 连接评估；由此确认早期组件测试通过不等于真实冷启动链路通过。

## 问题描述

真实冷启动暴露了三个相互关联但边界不同的问题：

1. 主任务要求启动 Ownward MCP；Governor 继承该项目配置后又尝试启动第二个 Ownward MCP。两个进程使用同一固定数据目录，子 Agent 的 MCP 握手退出，Governor 因而卡在启动阶段。
2. 主任务的项目配置可以与上层本机 transport 合并，所以只写启用开关仍能运行；首次修复却只在 `governor.toml` 中增加 `enabled = false` 和 `required = false`。Codex 会先把自定义 Agent 文件作为独立配置层校验，只有开关而没有 `command`、`args` 和 `cwd` 的 MCP 段不是完整 transport，因此出现 `invalid transport` / `malformed agent`，该修复不能生效。
3. 早期 Hook 测试是手动执行治理脚本，没有证明 Codex 会自动发现并调用项目 Hooks。本机旧 CLI 与尚未完成的精确 Hook 信任，使组件测试和新任务行为不一致。

同次端到端验证还遇到 Windows Sandbox 初始化失败，表现为只读命令也反复申请权限。该现象发生在 Hook 已自动触发、Governor 已成功启动之后，属于 Codex 本机执行环境故障，不是上述 MCP/Hook 治理设计缺陷，也不得据此继续改造治理状态机。

## 解决方案

1. 在项目 Governor 配置中保留 Ownward MCP 的完整 `command`、`args`、`cwd` transport，再明确设置 `enabled = false`、`required = false`。这样自定义 Agent 文件可以独立通过配置校验，同时 Governor 不会启动第二个产品 MCP。
2. 将本机 Codex CLI 更新到 `0.149.0`，通过官方 Hook 管理界面审阅并信任当前 `.codex/hooks.json` 的六个 Hook：`SessionStart`、`UserPromptSubmit`、`PreCompact`、`PreToolUse`、`PostToolUse`、`Stop`。
3. 使用全新会话而不是当前会话或脚本直调进行冷启动验收：确认 `SessionStart`、`UserPromptSubmit` 自动注入治理指令，Governor 配置无 `malformed agent` 警告，Governor 能返回结果，并确认没有第二个 Ownward MCP 进程。
4. `doctor` 必须机械检查 Governor 的只读属性、Hooks 未被整体关闭，以及项目存在 Ownward MCP 时 Governor 同名 MCP 段包含完整 transport 并明确禁用；同时验证待复核状态下的低成本读取仍被快速放行。

## 最终说明

本次 Governor 重复 MCP 与 Hook 未自动加载的问题已经修复并完成真实冷启动验证：六个 Hooks 均为 Active，`SessionStart` 与 `UserPromptSubmit` 已自动执行，Governor 能正常加载且没有启动第二个 Ownward MCP。

以后在同一环境中新建任务，预期直接进入自动 Hook → Governor 复核链，不再停在 MCP 握手，也不再出现 `invalid transport`。如果 Governor 或项目 MCP 定义变化，必须重新运行 `doctor` 和全新会话冷启动验收；如果 `.codex/hooks.json` 变化，Codex 会要求重新信任新哈希，这是正常安全行为。Windows Sandbox 初始化故障不在本修复范围内；再次出现时应作为 Codex 环境问题独立处理，不能误判为治理链路重新失效。
