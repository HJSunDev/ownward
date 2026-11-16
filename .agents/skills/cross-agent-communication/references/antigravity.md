# Antigravity 接收说明

## 原生入口

Antigravity App 提供向已有会话投递消息的 `agentapi` 命令。由目标会话提供稳定的 conversation ID，核对所属项目后调用：

```text
agentapi send-message <conversation_id> <正文>
```

外部调用需要 Antigravity 宿主提供的运行上下文。使用宿主管理的 Sidecar 承接调用；普通终端缺少上下文时不能直接使用，不读取其他进程的环境或提取认证信息。Sidecar 的配置与原生入口见[官方说明](https://antigravity.google/docs/sidecars/#agentapi)。

Sidecar 定义位于 `~/.gemini/config/sidecars/<sidecar-id>/sidecar.json`，由 `~/.gemini/config/config.json` 的对应项启用，并绑定目标所属的 `projectId`。保持目标会话身份不变，不使用 `new-conversation` 代替接收，也不通过修改模型或权限解决投递问题。

## 已部署入口

本项目的[Antigravity 本机接入](local-adapters.md#antigravity-本机接入)由宿主 Sidecar 监听本地请求，再调用上述原生命令。其他发送者只需调用本机投递工具，无需具备 Antigravity 的认证上下文。

按固定消息 ID 投递；同 ID 内容不一致时报错。`pending` 表示本地待处理，`dispatching` 表示已开始提交，`submitted` 只表示原生命令返回成功。对方实际读取后产生的关联回复才是接收证据，回复仍不等于业务任务验收通过。

`unknown` 或重启后遗留的 `dispatching` 不自动重发，先核对目标会话和原请求。更换目标绑定前处理旧请求，不能将旧信改投新会话。适配器未启动时保留待发消息并检查启用与宿主状态，不另起会话或模拟界面发送。

## 验证边界

已于 2026-11-13 在 Windows Antigravity App `2.31.2` 验证宿主加载本机接入、固定目标会话实际读取消息并返回关联回执。工作中投递是否立即进入当前回合、是否排队或造成中断，尚未验证；不能承诺回合内引导。本机绑定在 `collab/antigravity/config.json`，验证范围及回执引用在 `collab/antigravity/verification.json`。
