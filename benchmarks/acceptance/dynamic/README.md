# 动态未见验收

本目录实现候选冻结后的动态未见验收及组织结构增益对照。`protocol.json` 在候选版本中冻结生成规模、覆盖范围、模型角色、资源预算和统计阈值；具体语义世界、自然语言信息与查询只在候选提交和发布二进制固定后生成一次。

验收依次生成隐藏语义世界、生成自然语言表达、由独立模型验证表达与真值一致，再将同一份冻结资产和派生状态分别交给完整 Ownward 和同一二进制的无关系组织模式。两组使用相同模型、状态、查询、外部智能体和预算；基线只设置 `OWNWARD_DISABLE_RELATIONS=true`，加载时屏蔽关系组织状态、关系信号和关系导航，不重新生成其他语义状态。

正式运行会输出动态报告、组织消融报告及二者引用的原始证据。任一有效批次不得因产品结果不利而放弃或重跑；修改候选、产品配置、协议或生成数据后，原报告全部失效。

智能体答案只有在其信息标识与答案事实能够从成功的 Ownward 工具结果中直接复核时才计为正确；仅调用过工具或答案碰巧正确均不通过。

```powershell
python benchmarks/acceptance/dynamic/verify.py `
  --repository . `
  --binary <release-ownward.exe> `
  --candidate <full-commit-sha> `
  --evidence-dir <new-empty-evidence-directory> `
  --dynamic-output <dynamic-report.json> `
  --ablation-output <organization-ablation-report.json> `
  --codex-binary <native-codex-0.117.0-executable> `
  --codex-auth-file <auth.json> `
  --chat-model gpt-5.4-2026-03-05
```

模型服务凭证默认从 `OPENAI_API_KEY` 读取。中断后仅可对原候选、原协议、原二进制和原模型配置增加 `--resume`；任何绑定不一致都会拒绝续跑。

正式动态数据生成前，先用不进入最终证据的固定诊断样本分别实测一次生成、验证、信息组织和两种条件下的智能体查询，按协议中的场景数、信息数和八个智能体批次推算总耗时、调用量与外部费用。协议预算是异常上限，不是允许无条件等待的耗时计划；推算不可接受或实际阶段耗时明显偏离时立即停止，先修复执行路径。正式批次仍只生成一次，同一候选的组织消融和必要编排校准必须复用该批数据及未变化的运行结果。
