# 外部智能解耦

## 状态

架构解耦与并列适配已完成。OpenCode Go / Qwen3.8 Flash 是默认外部智能；原 Codex App Server / GPT-5.6 Luna、Terra 实现与既有配置保持可用。两者只能在启动前显式选择，运行中禁止切换或静默降级。

## 架构判断

产品内核已有稳定 `semantic-capability/v1` 与 `product-capability/v1`，问题不在内核。耦合发生在演进与验证控制层：业务编排曾直接依赖 Codex App Server、认证路径、进程错误、worker 调度和 Codex 命名的检查点，使更换外部智能必须同时改生成、语义、Reader、Judge、成本与恢复代码。

终态遵守[架构总纲](../architecture/overview.md)的窄腰：业务角色 → `ownward.external-intelligence/v1` → 具体适配器。业务角色保留自己的提示、Schema、工具预算、评分和完成条件；稳定端口统一结构化 turn、动态工具、用量、超时、重试、并发和恢复身份；供应商适配器独占进程、认证与事件格式。

## 实现与边界

- 通用端口与有界调度位于 `benchmarks/support/external_intelligence.py`。
- 唯一运行装配位于 `benchmarks/longmemeval_s/external_intelligence_runtime.py`，只负责校验版本化实现目录、选择一个适配器并封存身份。Codex 与 OpenCode 细节分别收敛在 `codex_external_intelligence.py` 和 `opencode_external_intelligence.py`；业务编排不导入二者。
- LongMemEval、非正式迭代、盲测生成/准入、Reader 与 Judge 通过稳定端口调用。正式质量协议保持原字节与既有兼容字段；独立版本化的运行选择清单封存 provider/driver，运行与请求身份再把二者合并。
- 供应商、模型、推理档位和执行制品仍是证据的直接依赖，解耦不允许身份模糊或静默降级。
- 既有产品内核、资产、正式 Acceptance state、当前模型与当前 Codex 认证方式均不改变。固定产品专项验收仍是其已封存的具体评测适配器，不属于本次未来动态生成/Reader 替换链，未被重写或冒充新证据。
- OpenCode 适配器为每个 worker 建立隔离、可清理的本机服务；所有内置工具默认关闭，只把本次声明的 Ownward 工具经带随机凭证的私有回环 MCP 桥接给模型。适配器从封存实现目录取得 provider、允许模型与档位，负责严格 JSON 解析、Schema 本地校验和有界重试，不再包含供应商或模型特判。认证内容只被复制到临时隔离目录，从不进入证据身份。
- `external-intelligence-runtime.json` 是封闭的实现目录而非通用插件系统。目录有一个默认 driver，但显式选择绑定到所选条目自身；新增或修改无关实现不会连带失效另一实现的证据。

## 外部能力核验

- OpenCode 的服务接口能够按请求指定 provider/model、variant、工具集合和会话，并可接入本地 MCP；接入不依赖私有补丁。
- OpenCode Go 实际目录确认 `qwen3.8-flash` 可用档位为 Medium 与 XHigh。冻结角色为：Generator、质量准入、语义和 Reader 使用 XHigh，Judge 使用 Medium。
- 独立代表验证全部通过：Generator `135.611 s`、质量准入 `30.572 s`、语义 `11.774 s`、Reader `17.175 s`、Judge `2.500 s`；质量准入一次调用正确接受 `2/2` 正例并拒绝 `6/6` 定向反例，零重试、零限流。
- 正式 500 题直接职责的保守成本投影含 20% 波动、10% 有界重试和 3,600 秒恢复余量为 `9,768.865 s`，低于 `20,400 s` 硬上限。该资格证明不改写正式 Acceptance state，也不冒充完整 500 题实测。

## 完成证明

- 端口与当前适配器具有独立成功、失败、身份、并发、原子检查点和零执行恢复测试。
- LongMemEval 与通用盲测/生成/准入流程共用同一执行器；后者不再调用 LongMemEval 私有能力实现，角色在稳定边界逐次传递并有反向测试保护。
- 业务主路径不再导入 Codex App Server 类型，也不直接解释当前供应商的执行字段；当前适配器错误在边界翻译为稳定错误。
- 请求身份覆盖提示、Schema、角色模型、推理档位、工具清单、基础指令、超时/重试和运行时身份；认证只绑定定位摘要，不读取内容。
- 运行时身份额外绑定所选适配器实现摘要和真实本地执行制品；未知 driver、不支持的模型/档位、制品或凭据缺失均在启动前失败。
- preflight、binding、运行身份和报告显式封存稳定合同及当前 provider/driver；正式协议不因纯适配器重构改写，直接依赖变化只使真实消费者证据失效。
- 角色资格分别覆盖验证题生成、独立质量准入、语义组织、Reader 主动工具闭环与 Judge；机器结果位于 `.tmp/external-intelligence/opencode-go-qwen3.8-flash-qualification-v2`、`...-v3` 与 `opencode-go-qwen3.8-flash-quality-admission-v2`。
- 稳定端口、双适配器、选择/身份隔离、Schema、失败和恢复测试通过；正式 Acceptance state、V0/V1 证据及当前产品均未改写。

## 下一动作

本支线到此结束。主线需要外部智能时使用默认 OpenCode Go / Qwen3.8 Flash；需要复现既有证据或对照时，在执行配置中显式选择 `codex-app-server/v1`。任何正式长运行仍须经过原有 preflight 与身份绑定，不能复用不同 provider 的检查点。
