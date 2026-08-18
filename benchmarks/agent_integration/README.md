# 外部智能体接入验收

本验收固定使用 Codex `gpt-5.4`（`low` reasoning）证明外部智能体能够从 Ownward 取得协作规则，并通过统一核心完成创建、检索、读取和更新；随后由全新会话确认相同信息身份和更新结果。验收器拒绝提示词变化及任何旁路工具，只保存提示摘要、Ownward 工具事实和合成验收资产，不复制系统提示或其他私有内容。

验收器使用同一候选二进制先后启动两个独立 Codex 会话。第一轮从 Ownward 取得规则并完成固定信息的创建、检索、读取和更新；第二轮验证跨会话读取与经验应用。两轮都使用空白工作目录和仅含临时认证副本的隔离 `CODEX_HOME`，关闭无关工具与扩展，临时认证副本随会话目录一并销毁。第一轮只预先批准固定合成数据的 `ownward_create` 与 `ownward_update`，不跳过其他权限检查；第二轮不预先批准任何写操作。

本轮验收固定使用两段内容：

```text
Ownward acceptance Project Borealis requires one action before migration: verify backups.
Ownward acceptance Project Borealis requires two actions before migration: verify backups; test restoration.
```

同一提示还必须原样提供以下 scratch note，并要求智能体只依据刚从 Ownward 取得的规则自行判断是否沉淀；提示不得直接告诉智能体预期判断，也不得转述相关规则。验收以它未出现在任何创建、更新或权威资产中为通过：

```text
Scratch note: the acceptance controller is currently considering retry 2; this belongs only to this session.
```

交互会话使用以下固定提示；其中没有预置 Ownward 的信息边界或检索规则：

```text
Use only the connected Ownward tools and do not use shell or file tools. First obtain Ownward's collaboration rules. Evaluate each statement below only under those rules.

Statement A: Ownward acceptance Project Borealis requires one action before migration: verify backups.
Statement B: Scratch note: the acceptance controller is currently considering retry 2; this belongs only to this session.

Search for Project Borealis before making any mutation. For each statement that belongs in Ownward under the retrieved rules, persist it through Ownward. Search for and read Statement A after creation. Then treat the following as the corrected full content of Statement A, update the same stable information item using its observed revision, and search for and read it again:

Ownward acceptance Project Borealis requires two actions before migration: verify backups; test restoration.
```

构建写入完整候选提交的发布二进制后执行：

```powershell
python benchmarks/agent_integration/verify.py `
  --binary bin/ownward.exe `
  --data-dir <temporary-root>/data `
  --codex-binary <codex.ps1> `
  --codex-auth-file <auth.json> `
  --candidate <commit-sha> `
  --output <agent-integration-report.json>
```

验收器会启动第二个独立 Codex 会话，仅通过 Ownward MCP 检索、读取已更新信息，并据此给出 Project Borealis 迁移前必须完成的全部行动；只复述记录而未正确应用不通过。候选二进制、权威资产和两次会话证据会绑定到报告。报告及其相邻的 `.mutation.jsonl`、`.independent.jsonl`、`.assets.jsonl` 必须一同保留，任何候选二进制、资产、提示或会话变化都会使结果失效。
