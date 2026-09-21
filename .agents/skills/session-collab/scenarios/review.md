# 剧本·独立复核（固定 2 角色，串行）

依据 [权威文档 §5.2](../../../docs/workbench/cross-session-collaboration.md)。

发起人=被审方（用户所在会话优先）。审查方对被审产物只读；运行测试/构建须持资源租约，验证绑定冻结快照。

| 相位 | 动作与约束 | 产物 |
| --- | --- | --- |
| request | 被审方写目标、范围、变更内容/文件、验收标准、已知风险、审查重点；被审版本冻结（request.md 记录哈希，机械可检测） | `request.md` + 冻结快照（expectation: review） |
| independent-review | 审查方独立读材料、跑验证；**findings 落盘前不得与被审方讨论**；每条含严重度/证据/影响/建议/置信度 | `findings/<reviewer>.md` |
| disposition | 被审方逐条 accept\|reject\|defer + 理由 + 证据；禁止无理由全盘接受 | `disposition.md` |
| fix | 修复 accepted 项；写改了什么、没改什么、为什么 | `changes.md` |
| verify | 审查方独立验证修复（不能只看摘要）；可 reopen | `verification.md` |
| close | 全部发现已处置且**全部 blocker 已关闭**方可 closed；摘要顶部列未关闭 major/minor 与残余分歧 | 关闭状态消息 |

**blocker 关闭仅三条路径**：①修复并经审查方 verify；②审查方书面接受降级并附证据；③用户明确接受并记录。被 reject 的 blocker 不得视为关闭——升级用户，未决则 dead-letter。

**冻结失效**：被审版本身份变化即失效，必须重审；提交时核对提交哈希与冻结哈希一致。

**无否决权**：审查方无 veto；被审方拥有最终决定权，异议保留、高严重度分歧不得埋没。

**发现条目格式**：

```markdown
- [<blocker|major|minor>] <标题>
  - 位置：<文件:行 或 缺失点>；证据：<为什么是问题>；影响：<后果>
  - 建议：<修复方向>；置信度：高|中|低
```
