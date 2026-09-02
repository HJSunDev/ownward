# 外部智能解耦

## 状态

已完成。当前供应商、模型与正式评测结果均未切换；本工作包只建立稳定边界。

## 架构判断

产品内核已有稳定 `semantic-capability/v1` 与 `product-capability/v1`，问题不在内核。耦合发生在演进与验证控制层：业务编排曾直接依赖 Codex App Server、认证路径、进程错误、worker 调度和 Codex 命名的检查点，使更换外部智能必须同时改生成、语义、Reader、Judge、成本与恢复代码。

终态遵守[架构总纲](../architecture/overview.md)的窄腰：业务角色 → `ownward.external-intelligence/v1` → 具体适配器。业务角色保留自己的提示、Schema、工具预算、评分和完成条件；稳定端口统一结构化 turn、动态工具、用量、超时、重试、并发和恢复身份；供应商适配器独占进程、认证与事件格式。

## 实现与边界

- 通用端口与有界调度位于 `benchmarks/support/external_intelligence.py`。
- 当前 Codex App Server 装配位于 `benchmarks/longmemeval_s/external_intelligence_runtime.py`；它是唯一了解 Codex 传输的活动 LongMemEval/Stage 6 运行边界。
- LongMemEval、非正式迭代、盲测生成/准入、Reader 与 Judge 通过稳定端口调用。正式质量协议保持原字节与既有兼容字段；独立版本化的运行选择清单封存 provider/driver，运行与请求身份再把二者合并。
- 供应商、模型、推理档位和执行制品仍是证据的直接依赖，解耦不允许身份模糊或静默降级。
- 既有产品内核、资产、正式 Acceptance state、当前模型与当前 Codex 认证方式均不改变。固定产品专项验收仍是其已封存的具体评测适配器，不属于本次未来动态生成/Reader 替换链，未被重写或冒充新证据。

## 完成证明

- 端口与当前适配器具有独立成功、失败、身份、并发、原子检查点和零执行恢复测试。
- LongMemEval 与通用盲测/生成/准入流程共用同一执行器；后者不再调用 LongMemEval 私有能力实现，角色在稳定边界逐次传递并有反向测试保护。
- 业务主路径不再导入 Codex App Server 类型，也不直接解释当前供应商的执行字段；当前适配器错误在边界翻译为稳定错误。
- 请求身份覆盖提示、Schema、角色模型、推理档位、工具清单、基础指令、超时/重试和运行时身份；认证只绑定定位摘要，不读取内容。
- preflight、binding、运行身份和报告显式封存稳定合同及当前 provider/driver；正式协议不因纯适配器重构改写，直接依赖变化只使真实消费者证据失效。
- 稳定端口 6 项、LongMemEval 适配器 61 项、相关 Acceptance 与冻结 Stage 4 精确迁移保护均通过；Acceptance `check` / `self-check` 通过。本工作包没有运行模型或改写正式状态。

## 下一动作

需要切换供应商时，按 `benchmarks/support/README.md` 新增一个适配器并先做角色资格与成本校准；在此之前不选择 OpenCode、DeepSeek 或其他实现。
