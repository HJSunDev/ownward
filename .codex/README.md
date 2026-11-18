# 开发会话的 Ownward MCP

从已构建的 Ownward 检出目录或其子目录打开 Codex。项目配置用 Git 的 shell 别名在仓库根执行 `./bin/ownward mcp --data-dir .ownward/codex`。Windows 上由 Git for Windows 启动同目录的 `ownward.exe`。程序直接使用标准输入和输出。

搬迁检出目录时，可用 `codex mcp get ownward --json` 检查生效的工作目录。若自己的用户配置为这个服务指定了 `cwd`，请将该值更新到新的检出位置，或移除该项以沿用当前工作目录。

主会话启用该服务。非产品子 Agent 保留完整的 transport 定义，但禁用服务，以免打开用户数据。构建入口见[项目说明](../README.md)。
