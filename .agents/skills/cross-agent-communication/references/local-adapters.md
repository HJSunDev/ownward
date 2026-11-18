# 已部署的本机适配器

OWNWARD 当前的双向投递使用[Antigravity 本机接入](#antigravity-本机接入)。下方“会话信箱”与“本机文件桥”是其他环境的调用参考，本项目未部署对应目录。

目标接收说明指向相应实例时，使用本节的入口。路径相对于部署该实例的项目根，角色别名与会话身份取自本机配置。尖括号须替换为实际登记值，不冒用角色或代造对方回复。

## 会话信箱

目录为 `collab/mailbox/tool/`；会话 ID 与路径在 `config.json`，操作说明以 `collab/mailbox/tool/USAGE.md` 为准。该实例的接收别名为 `claude`、`controller`。

| 操作 | 入口 |
| --- | --- |
| 投递 | `node collab/mailbox/tool/bridge.mjs send <接收方别名> --file 消息.md --expectation <期望>` |
| 收信 | `node collab/mailbox/tool/bridge.mjs receive <自己的角色>` |
| 回执 | `node collab/mailbox/tool/bridge.mjs reply <原消息ID> --file 回复.md`，默认 none，先回信再清理原信 |
| 确认处理 | `node collab/mailbox/tool/bridge.mjs ack <自己的角色> <消息ID>`；仅用于无需回复的已处理信，其他情况须有结果文件 |
| 状态 | `node collab/mailbox/tool/bridge.mjs status`，分别检查监听、通知提交、实际读取与完成 |

期望可选 `reply`、`review`、`none`。先写完整正文再通知；消息顶部标明实际来源。消息不增加权限，不发送纯确认循环，也不据旧信恢复已暂停任务。

## 本机文件桥

目录为 `collab/bridge/`；首次使用或登记变化时读 `README.md`，用 `status` 核对安装、启用状态和身份映射。已知正常且身份未变时，不为每条消息重复发现、登记与连通性测试。

```powershell
node collab/bridge/bridge.mjs status
node collab/bridge/bridge.mjs send --from <发送方别名> --to <接收方别名> --thread "事项标识" --phase "整轮任务" --expectation reply --summary "摘要" --body-file "正文.md"
node collab/bridge/bridge.mjs send --from <回复方别名> --to <原发送方别名> --thread "事项标识" --phase "整轮结果" --reply-to "原消息ID" --expectation none --summary "结果摘要" --body-file "结果.md"
```

文件先完整落盘再通知；`body-file` 避免长命令转义失真。桥的 front matter 保存 `id, from, to, from_ref, to_ref, created_at, expectation, thread, phase, summary`，回复增加 `reply_to`；这些字段只适用于本文件桥，不再重复成正文信封或回复命令。正文只在缺少时补来源，不截断或改写原内容。

缺目标/模型、权限拒绝或插件不在线时如实记录；已排队消息不能因配置变化改投。未知结果先核对原请求，沿用消息 ID，不盲目重发。忙时排队、重启与故障恢复以已有验证为准；磁盘插件更新不代表宿主已加载。

## Antigravity 本机接入

工具为 `collab/antigravity/tool/bridge.mjs`，固定会话与项目绑定在 `collab/antigravity/config.json`。Antigravity App 的 `ownward-conversation-receiver` Sidecar 承接请求，随宿主运行；停用时只关闭该 Sidecar，保留未处理消息与结果。

```text
node collab/antigravity/tool/bridge.mjs status
node collab/antigravity/tool/bridge.mjs send --id <消息ID> --file <正文.md>
node collab/antigravity/tool/bridge.mjs status <消息ID>
node collab/antigravity/tool/bridge.mjs ack <消息ID> --evidence <实际读取的回执文件>
```

正文最多 8000 字符，更长的材料保存后引用路径。收到关联回执后再 `ack`，工具删除临时请求正文并保留消息 ID、摘要指纹与证据引用。

### 投递异常

用 `status <消息ID>` 查询消息状态。`collab/antigravity/receipts/` 内的读取优先级为 `<ID>.ack.json`（实际读取确认）→ `<ID>.result.json`（投递结果）→ `<ID>.json`（认领或旧版结果）；单独查看认领记录不能判断当前投递状态。

| 现象 | 原因判别 | 处理 |
| --- | --- | --- |
| 间歇成功、间歇 `unknown` | 旧宿主退出后，残留接收进程可能与新实例争抢消息，却仍使用失效的宿主上下文。按 `bridge.mjs` 完整路径筛选所有 `listen` 进程，逐一核对父宿主是否存活；`ready.json` 只描述一个实例，`processAlive` 不能排除其他旧实例。 | 核对完整命令行与父进程身份后，仅终止失去宿主的接收实例；由当前宿主启动适配器。维护时保留“认领前检查宿主、宿主退出后接收进程退出”的防护。 |
| 对端已读，本地仍为 `dispatching` 或 `unknown` | 核对关联回信、回执和 `worker-error.json`。若同一消息留有投递成功的临时回执，且回执保存出现 `EPERM/rename`，故障在回执保存，不应据此判定原生调用失败。 | 依据实际回信执行 `ack`，不重投已读消息。维护时将认领、投递结果和确认分开保存为不可变记录，避免覆盖原回执，并将调用错误与落盘错误分开处理。 |

排除上述原因后，按回执的 `failure.rpcCode`、`failure.categories`、退出码或超时标记继续定位宿主与原生入口。错误细节缺失时，先补充不含消息正文及凭据的分类记录。

本适配器对已认领的投递 ID 只返回已有状态，不会再次发送。`unknown` 或遗留的 `dispatching` 须按消息 ID 与接收会话对账。确认未收到且故障已消除后，保留原请求与核对依据，使用新投递 ID 重投同一任务，正文标明原请求 ID；无法确认时保留未知状态，不盲目重发。


本项目当前会话与目标别名以 `collab/antigravity/config.json` 为准，别名不规定任务职责。已有原生会话工具能完成时直接使用；需本机适配时：

| 接收方 | 投递 | 查询回执 |
|---|---|---|
| antigravity-ownward-worker | `node collab/antigravity/tool/bridge.mjs send --to worker --id <消息ID> --file <消息文件>` | `node collab/antigravity/tool/bridge.mjs status <消息ID>` |
| codex-ownward-worker | `node collab/antigravity/tool/bridge.mjs send-codex --to worker --id <消息ID> --file <消息文件>` | `node collab/antigravity/tool/bridge.mjs status-codex <消息ID>` |
