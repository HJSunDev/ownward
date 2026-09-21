# 消息文件 front matter 模板

文件名：首消息固定 `request.md`；其余 `YYYYMMDDTHHMMSSfff-<sender-slug>-<phase>.md`。路径相对线程目录。

```yaml
---
thread: <thread-id>
msg: <本消息文件名>
from: <角色slug>
to: <角色slug>
state: open|waiting|closed|dead-letter
close_reason: -          # 终态必填：completed|redundant|timeout|rejected-blocker|user-decision-needed
sedimentation: -         # 终态必填：<records-id|文档路径|none>（沉淀落点）
phase: <剧本相位>
scenario: <剧本名>
reply_to: <被回复消息文件名>   # 无则 "-"
expectation: reply|draft|review|none   # 省略默认 reply；知会必须显式 none
artifacts: [<相对线程目录的产物路径>]
summary: <一句话>
---
<正文>
```

**线程首消息（request.md）另加**：

```yaml
initiator: <角色slug>
participants: [<角色slug>, ...]
roles: [{slug: <角色slug>, lens: <镜头>, write: true|false}, ...]
success-criteria: <一句话>
```

**降级模式（无共享文件系统）**：消息体显式携带 `state/phase/reply_to/expectation/artifacts`，首消息标注"降级"。
