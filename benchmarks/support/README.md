# 评测支撑边界

本目录只放多个评测入口共享、但不属于任何具体供应商的执行契约。

`information_use_flow.py` 仅转接[产品接入组件](../../integrations/python/ownward_information_use.py)，真实智能体与模拟 Reader 复用同一实现。标准 `respond` 默认提供能力说明；已有检索循环使用同一 `OFFER`、`RESPONSE` 与 `finish`，由智能体在原响应中选择是否协作，直接路径不增加调用。关卡与最终测试的公共 Reader 已接入此选择；调用方交付原任务、实际已获材料及独立上下文的 `invoke`，沿用模型、档位、规则和留痕，不按题号分派。使用契约与验证边界见[模块说明](../../docs/modules/information-use/README.md)。

`external_intelligence.py` 是生成、语义组织、Reader 和 Judge 共用的稳定外部智能端口。业务编排只能依赖它提供的结构化 turn、动态工具回调、用量、超时、错误和有界并发语义，不能导入供应商进程、认证或事件类型。该端口同时统一有界重试、原子检查点、失效归档和字节级恢复；具体业务只提供自己的提示、Schema、校验和可选工具会话钩子，不再复制执行生命周期。

所有新判分（含关卡、最终测试、临时实验和质量准入）统一使用 **xhigh**；共享执行入口覆盖旧配置中的较低档位，并按实际档位保存请求身份。历史判分记录不改写。

唯一装配入口是 `benchmarks/longmemeval_s/external_intelligence_runtime.py`。它从 `external-intelligence-runtime.json` 的版本化实现目录中选择一个 driver；Codex 与 OpenCode 的进程、认证、事件和清理分别留在各自适配器。供应商、模型、推理档位、适配器实现、执行制品、工具清单、超时/重试和并发都进入请求或运行身份，认证内容永不进入证据。目录默认是 `opencode-go-api/v1`（轻量客户端 / 阿里百炼 / Qwen3.8 Flash，driver 保留历史兼容标识）；旧 `codex_*` 配置仍被解释为显式 Codex 选择，行为不变。

装配时只加载所选实现；各实现通过公共端口复用 JSON 校验与检查点能力，不互相导入。未选实现的代码变化不改变所选实现身份，共享执行逻辑变化仍如实计入身份。

关卡与最终测试通过各自数据和协议调用同一运行器；切换入口无需修改公共代码。历史审计按记录的摘要读取冻结输入（当前文件或 Git 历史），不要求当前源码、协议或产品清单退回旧版，也不执行历史代码；历史测量若重新执行，仍须核验其原执行依赖。当前运行的制品、协议、角色及结果身份由当前入口独立校验。CI 保留完整 Git 历史供历史审计和 V0 制品构建使用。

OpenCode 适配器使用文本 JSON 和严格 Schema 校验，不依赖原生结构化输出接口。格式不合规时，在同一会话、原时限内最多请求一次格式纠正，禁止新增工具调用；前后响应与纠正请求分别留存，Token 和耗时累计，`format_corrections` 单独计数。再次不合规仍按失败处理，不能截断字段或放宽 Schema。

`opencode-go-api/v1` 是第三个轻量实现，模型为 `qwen3.8-flash`。一个宿主进程承载多路独立请求，不启动 OpenCode 或工具桥进程；沿用相同角色档位、产品规则、工具回调及公共重试/检查点流程。配置中的 `binary` 指向 `benchmarks/longmemeval_s/go_api_external_intelligence.py`。`credential_file` 仍可指向原百炼密钥 JSON（`{"BAILIAN_API_KEY":"填入套餐专属密钥"}`），也可指向下述服务组合配置。密钥只由客户端读取并发送给其对应服务，不复制到组合文件或请求记录。请求、流式响应和工具结果逐步留存，格式纠正仍限一次。另外两个 driver 及其认证方式保持不变。

两项 Messages 服务共用轻量客户端，各自独立配置；`service_routing.py` 只负责可移除的切换策略。每道题创建独立 scope，默认百炼；明确返回 `data_inspection_failed` 时，只将被拒绝的请求连同原上下文转给 Go，该题后续调用沿用 Go。新题重新从百炼开始，并发题互不影响。切换沿用原时限、模型、档位、规则和工具结果，不重复已执行的工具，不改变运行身份。Go 也失败则保留两次失败现场，交给原有有界恢复流程。没有显式 scope 的独立调用每次从 primary 开始。

本机组合配置为仓库外的 `E:/Ownward/credentials/lightweight-services.json`，示例如下；相对凭据路径按组合文件所在目录解析。移除 `fallback` 即关闭组合；将 `primary` 改为任一服务即可单独使用，未选服务的凭据无需存在。

```json
{
  "schema": "ownward.messages-services/v1",
  "primary": "bailian",
  "fallback": {"service": "opencode-go", "on_error_codes": ["data_inspection_failed"]},
  "services": {
    "bailian": {
      "url": "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic/v1/messages",
      "credential_file": "bailian.json",
      "key_path": ["BAILIAN_API_KEY"]
    },
    "opencode-go": {
      "url": "https://opencode.ai/zen/go/v1/messages",
      "credential_file": "C:/Users/name/.local/share/opencode/auth.json",
      "key_path": ["opencode-go", "key"],
      "session_header": "x-opencode-session"
    }
  }
}
```

默认配置只需提供轻量客户端文件和本机认证定位；语义与 Judge 为 medium，Reader、生成与准入为 xhigh，采用已验证的角色配置。关卡与最终测试共用此默认；本机当前执行配置为 `.tmp/final-test-ready/execution.json`：

```json
{
  "external_intelligence": {
    "binary": "E:/path/to/ownward/benchmarks/longmemeval_s/go_api_external_intelligence.py",
    "credential_file": "E:/Ownward/credentials/lightweight-services.json"
  }
}
```

若要选择原 Codex 实现，在同一块中写入 `"driver": "codex-app-server/v1"` 并提供 Codex 制品和认证定位；省略 roles 时使用原 Luna/Terra 冻结组合。选择 OpenCode 进程版则指定 `"driver": "opencode-server/v1"` 和对应 OpenCode 制品。一次运行内 driver、每项职责的模型和档位均固定；轻量端的同模型服务切换按上述局部策略执行。

OpenCode 进程版的 Qwen3.8 Flash 五类代表资格均通过；历史投影为 `9,768.865 s < 20,400 s`，不作为轻量版或完整评测的耗时结论。

接入另一种外部智能时只做四件事：

1. 实现 `ExternalIntelligenceTransport`，完整支持结构化输出、需要的工具闭环、用量和失败语义。
2. 在唯一实现目录和装配入口登记新 driver，并让适配器在打开进程或网络前校验制品、认证定位和并发边界。
3. 在唯一运行选择清单中显式选择 provider/driver，在角色协议中显式选择模型与推理档位；不得改生成、语义、Reader、Judge、评分或恢复流程。
4. 通过端口合同、角色资格、成本、失败开放和字节级恢复验证后，才允许把新身份用于候选证据。

若新实现不能提供动态工具、多步主动检索、严格 Schema、可审计用量或有界恢复，它不是当前评测角色的等价替换，必须失败关闭。

OpenCode Go / Qwen3.8 Flash 的资格命令（只写非正式输出，不触碰 Acceptance state）：

```powershell
python benchmarks/longmemeval_s/opencode_qualification.py `
  --binary E:/path/to/opencode.ps1 `
  --credential-file C:/Users/name/.local/share/opencode/auth.json `
  --output-dir .tmp/external-intelligence/opencode-go-qwen3.8-flash-role-qualification
```
