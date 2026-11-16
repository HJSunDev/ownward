# 持久任务与消息记录

需要跨回合恢复、交审或接续时，在 `collab/threads/<YYYYMMDD>-<topic>-<4位随机十六进制>/` 使用此格式。首消息为 `request.md`，后续消息为 `YYYYMMDDTHHMMSSfff-<sender>-<phase>.md`。已有任务材料用路径引用。

```yaml
---
thread: <thread-id>
msg: <本文件名>
from: <角色ID>
to: <接收角色ID>
state: open|waiting|closed|dead-letter
phase: <当前工作阶段>
scenario: <协作场景>
reply_to: <所回复的消息文件名，无则 "-">
expectation: reply|draft|review|none
artifacts: [<相对线程目录的产物路径>]
summary: <本次结论或所需行动>
---
<正文>
```

需要回复时使用 `reply`、`draft` 或 `review`；知会显式写 `none`，省略时默认 `reply`。`artifacts` 无内容可省略；引用材料须先保存，再发送通知。

首消息增加：

```yaml
initiator: <协调角色ID>
participants: [<参与角色ID>]
roles: [{slug: <角色ID>, lens: <职责或关注点>, write: true|false}]
success-criteria:
  - {statement: <完成要求>, judge: roles|user, check: <可核验条件>}
```

`write: true` 表示获分配的实施职责，实际写入还须取得租约。客观标准由各方核验；`judge: user` 只用于用户偏好或明确保留的验收，不能代判。

任务输入按[任务书保真](../../../../docs/workbench/cross-session-collaboration.md#312-任务书保真中继失真防护)组织：原始需求与事实上下文分开，注明独立或咨询模式；独立任务增加一行 `materials: user-specified|initiator-selected|mixed; selection: <筛选范围与重要排除项的依据>`。参与者可自行补证。

终态消息增加：

```yaml
close_reason: completed|redundant|timeout|rejected-blocker|user-decision-needed|unmet
sedimentation: <正式产物路径或none>
```

正文引用各方验收证据，列出残余问题；受阻则列未达项、证据、阻碍和下一步。`completed`、`redundant` 用于 `closed`，其余原因用于 `dead-letter`；等待回复或裁决时保持 `waiting`。具体完成条件与重开规则见[状态与推进](../../../../docs/workbench/cross-session-collaboration.md#35-状态与推进)。

没有共享文件系统时，使用接收方可访问的消息或产物链接，交接中带清当前状态、引用关系、所需回复与成果；不能引用对方不可访问的本机路径。
