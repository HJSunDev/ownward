# Codex 接收说明

## 接收入口与结果

使用[投递脚本](../scripts/codex-delivery.mjs)连接当前桌面实例的 IPC，按固定 thread UUID 发现会话所属客户端，再调用 `thread-follower-steer-turn` 向当前回合追加输入；不启动第二个执行者，不读取凭据，不修改模型或权限。

回执的`mode: steer`和`turnId`表示宿主接受了回合内引导，实际在回合结束前读到消息才证明效果。`mode: queue-idle`只用于宿主明确拒绝引导且说明会话已空闲的情况。管道不可达或找不到所属客户端，并不能证明所有运行环境中的目标会话都空闲；此时保留原信并报告失败，不静默退回排队。桌面未运行或尚未持有目标会话时，本通道等待它恢复，不另外启动会话。

未知结果重试保持同一消息标识；监听进程或发送方换代不改变消息身份。宿主未承诺用`clientUserMessageId`保证恰好一次，因此业务仍以信件ID、receive和ack确认幂等处理，不能仅凭投递标识相同声称无重复。

已验证环境：Windows Codex 桌面版 `26.1103.4239.0`。目标会话在同一活动回合结束前实际收到消息，回执为 `mode: steer`，且 `turnId` 一致。宿主升级后出现协议不匹配时重新核验。

## 现有本机调用

原生会话工具可直接完成时直接使用；需要适配时，选用已部署实例或其中复用的[投递脚本](../scripts/codex-delivery.mjs)，不能把本机配置当成其他环境现成可用。

- [会话信箱](local-adapters.md#会话信箱)：接收别名 `controller`，具体 ID 在 `collab/mailbox/tool/config.json`。
- [本机文件桥](local-adapters.md#本机文件桥)：Codex 身份在 `collab/bridge/config.json` 的 `controller.thread`。保留消息 ID、模式与活动 turnId；空闲投递使用本机已确认版本的 `codex queue`，不盲用 PATH 中另一版本。

不得以 resume、exec、新会话或第二个执行者替代原会话接收。实例与角色发生变化时重新核对绑定，不按旧岗位别名误投。
