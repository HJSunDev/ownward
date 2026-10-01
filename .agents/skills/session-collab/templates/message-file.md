# 消息文件 front matter 模板

文件名：首消息固定 `request.md`；其余 `YYYYMMDDTHHMMSSfff-<sender-slug>-<phase>.md`。路径相对线程目录。

```yaml
---
thread: <thread-id>
msg: <本消息文件名>
from: <角色slug>
to: <角色slug>
state: open|waiting|closed|dead-letter
close_reason: -          # 终态必填：completed|redundant|timeout|rejected-blocker|user-decision-needed|unmet
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
success-criteria:
  - {statement: <可判句>, judge: roles|user, check: <判据／证据锚点>}
```

`check` 写可核验条件及预期证据；允许忠实于需求的验收细化，不得缩减目标或指定解法。客观项用 `roles`；用户偏好或用户明确保留的验收用 `user`，不得代判满意，也不阻止角色在授权内修复客观缺陷。候选交付不等于用户采纳；用户验收若属于本任务标准，未获裁决就仍未达标。

**任务正文**（按 [权威文档 §3.12](../../../../docs/workbench/cross-session-collaboration.md)）：

```text
【用户原话】（适用任务机械复制；无法照录标【转述·保真降级】）
<原文>
【任务上下文】
<目标／事实与材料／有效约束／验收细化，各附依据>
模式：<独立／咨询；混合任务按子任务分区，纯执行或事实传递据实标明>
materials: user-specified | initiator-selected | mixed；selection: <筛选范围；已知重要排除项及依据，无则注明>
<独立：发起人观点留在己方草案；咨询：可另列【推断】、推荐与反对理由>
```

用户要求独立/共同发现/并行设计、采用独立复核，或无既定候选且需覆盖未知问题空间，任一成立即独立模式；混合任务独立判断落盘前不读他区推荐。材料来源行仅独立任务必填，筛选说明不证明完整公正，参与者可自行补充证据。已知故障与被审对象完整提供；事实可以澄清，猜测与方案偏好不作共同前提。每项限制须有适用依据，不能把可争辩判断包装成必选项。

**完成核验**（终态消息正文）：逐项列成功标准、实际证据/产物锚点、结论；`roles` 引用验证证据，`user` 引用用户裁决。全部标准、任务要求的独立参与与剧本安全/验证要求满足才可 `completed`，同意、轮次或 findings 已处置不能代替达标，也不得改写标准规避缺口。缺口未消除按 §3.5 继续处理或如实报告阻碍；等待用户裁决先 `waiting`，超时按 §3.7 处理。`dead-letter: unmet` 必须附未达项、证据、阻碍和单一推荐；线程级 `redundant` 仅用于重复/被替代且义务已移交，引用承接结果。终态仍须填写 `sedimentation`。

**降级模式（无共享文件系统）**：消息体显式携带 `state/phase/reply_to/expectation/artifacts`，首消息标注"降级"。
