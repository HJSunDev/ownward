---
name: session-collab
description: 在任何跨会话智能体协作场景使用：跨会话发消息、多角度分析、独立复核、一般协作与资源协调。权威设计见 docs/workbench/cross-session-collaboration.md。
---

# 跨会话协作（执行摘要）

本文件是 [权威文档](../../../docs/workbench/cross-session-collaboration.md) 的派生摘要；冲突时以权威文档为准。

## 核心纪律

1. **文件为准，消息只是中断**：先落盘，再 `session_send` 通知（`wake=true` 触发对方处理）。
2. **串行=发送后立即结束回合**，向用户报告一句后停止；禁止 sleep/轮询/反复读取对方会话。零轮询≠零等待。
3. **一问一答闭环**：最终回复只认 结果/拒绝/dead-letter；"排队"是临时 ack；不得静默。
4. **每回合只核对自己** `collab/roles/<slug>.md` 的 `open_threads`，禁止全 threads/ 扫描。
5. **宿主规则优先**：AGENTS.md 等常驻规则优先于本协议；线程与登记不放密钥。
6. **1+1>2，三角色时 1+1+1>3**：任何"接受"附理由与证据（不盲从）；不为一致折中；收敛前写 delta（对方贡献+我因何改变）；异议（dissent）永久保留；扩展角色必须过价值门（书面证明第三镜头独特价值）并经 `unique_contributions` 审计，无独特贡献标 redundant。
7. **单写者**：代码与权威文档只有发起人（实现方）可写；扩展角色一律只读，只写线程内自己的产物；**改代码前先原子获取 `workspace-write` 槽**（仅发起人可持有；验证租约有效期间不授予）；违反=blocker。
8. **资源协调**：`heavy`（构建/测试/批量/本地模型推理等 ≥1核或≥500MB或≥10s）与 `external`（外部付费 API）动作前必须持资源槽；本地重动作总量=1；协调范围=共享同一 `collab/` 工作区根的协作方，跨工作区不在范围。
9. **用户可见性**：只有发起人与用户对话；禁止把其他角色原文直接倾倒给用户。
10. **决策门＝必议＋单点负责**（权威文档 §3.11，判据以 §3.11 分级段为准）：D2 先咨询后动、D3 协作+按需升级用户、D1/执行单干但留痕；义务主体合成后单独拍板、对结果负责；咨询带紧迫度（needed-by）与"未回复默认处置"（仅限可逆）与决策日志（D2 无日志=未完成协作）；阻塞型咨询**两段式回复**——受询方被唤醒第一回合内快速表态（仅解锁对方可逆推进），完整分析随后回合补全；应协作未协作=违反（可重开）。**义务与登记角色绑定**（当前发起人义务主体=donald-knuth；未登记角色/外部智能体不受约束，但登记角色采纳其产出仍属自身 D2）；审查方 findings 与独立判断不受决策门约束；决策门不管辖回答/解释类陈述（证据纪律由宿主常驻规则约定）。

## 消息信封（聊天通道）

```text
【协作信封】
发件：<角色名>｜<会话ID>
收件：<角色名>｜<会话ID>
传输：串行 ｜ 并行
期望：reply ｜ draft ｜ review ｜ none
场景：<剧本>（thread: <线程ID>，phase: <相位>）
事项：<一句话>
────────────────────────────
<正文>
────────────────────────────
回复约定：完成后用会话发送回 <发件角色名>（<会话ID>），wake=true；
内容=结论+证据+遗留分歧；无法承担回"拒绝+原因"，不得静默。
```

## 场景剧本

| 场景 | 何时用 | 角色数 | 细则 |
| --- | --- | --- | --- |
| 多角度分析 | 开放问题需要根因/方案/风险多视角 | 2-3（默认2，价值门过才3） | [scenarios/multi-perspective.md](scenarios/multi-perspective.md) |
| 独立复核 | 已完成工作需要独立检查 | 固定2 | [scenarios/review.md](scenarios/review.md) |
| 一般协作 | 传信息、简单问答 | 固定2 | [scenarios/general-collaboration.md](scenarios/general-collaboration.md) |

用户不选场景；发起人（用户所在会话）按意图选择，写入 `request.md` 并一句话告知用户。

## 快速上手

1. `session_list` 识别当前会话；维护 `collab/roles/<slug>.md`（身份、映射、状态、open_threads）。
2. 新线程：`collab/threads/<YYYYMMDD>-<topic-slug>-<4位十六进制>/`；首消息固定命名 `request.md`（front matter 另加 `initiator/participants/roles/success-criteria`）。
3. 其余消息文件：`YYYYMMDDTHHMMSSfff-<sender>-<phase>.md`；front matter 按 [templates/message-file.md](templates/message-file.md)。
4. 通知只含：thread-id、phase、文件路径、一句话摘要、reply_to、expectation。
5. 重资源动作：原子 `mkdir collab/resources/slots/<class>/slot-1` 即持有（class=heavy|external|workspace-write；workspace-write 仅发起人、写代码/权威文档前获取，验证期间不授予）；写 `claims/<lease_id>.md`（[templates/lease-claim.md](templates/lease-claim.md)）；完成后移入 `claims/done/` 并删槽。抢占是协作式；回收前核对持有者 busy。
6. 降级（无共享文件系统）：消息体自包含 `state/phase/reply_to/expectation/artifacts`，首消息标注"降级"。
7. 关闭线程：终态消息必填 `sedimentation`（沉淀落点：records-id/文档路径/none）；`collab/` 为本地运行态不入库；终态 +30 天归档、归档 +60 天删除（清扫挂自然回合；永不清理清单与 tombstone 见权威文档 §7.3）。
8. 决策门（§3.11）：决策先按性质分级（D1 执行内选择单干留痕／D2 路径决策先协作／D3 重大不可逆协作+升级用户），再按决策类型路由场景（快速咨询→一般协作、开放设计→多角度分析、产物检查→独立复核）；同类咨询一次 ≤5 条；D2 结论必须落工作产物"决策"段（模板 [templates/decision-log.md](templates/decision-log.md)）；回复三步硬清单=先落盘→`session_send`(wake=true)→更新自己角色文件 `open_threads`。**紧急排障（用户催办）用事件级批量咨询**：事件开始先发后动（边界＋有界可逆步骤类＋事件级默认处置＝对方首回合无异议才执行该类），结束下回合批量追审＋完整日志（缺日志即违规）。
