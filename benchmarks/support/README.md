# 评测支撑边界

本目录只放多个评测入口共享、但不属于任何具体供应商的执行契约。

`external_intelligence.py` 是生成、语义组织、Reader 和 Judge 共用的稳定外部智能端口。业务编排只能依赖它提供的结构化 turn、动态工具回调、用量、超时、错误和有界并发语义，不能导入供应商进程、认证或事件类型。该端口同时统一有界重试、原子检查点、失效归档和字节级恢复；具体业务只提供自己的提示、Schema、校验和可选工具会话钩子，不再复制执行生命周期。

唯一装配入口是 `benchmarks/longmemeval_s/external_intelligence_runtime.py`。它从 `external-intelligence-runtime.json` 的版本化实现目录中选择一个 driver；Codex 与 OpenCode 的进程、认证、事件和清理分别留在各自适配器。供应商、模型、推理档位、适配器实现、执行制品、工具清单、超时/重试和并发都进入请求或运行身份，认证内容永不进入证据。目录默认是 `opencode-server/v1`（OpenCode Go / Qwen3.8 Flash）；旧 `codex_*` 配置仍被解释为显式 Codex 选择，行为不变。

默认配置只需提供本机 OpenCode 制品和认证定位；角色使用已经通过资格验证的默认档位：

```json
{
  "external_intelligence": {
    "binary": "E:/path/to/opencode.ps1",
    "credential_file": "C:/Users/name/.local/share/opencode/auth.json"
  }
}
```

默认档位为 Generator/质量准入/语义/Reader `qwen3.8-flash/xhigh`，Judge `qwen3.8-flash/medium`。若要切回原实现，在同一块中写入 `"driver": "codex-app-server/v1"` 并提供 Codex 制品和认证定位；省略 roles 时使用原 Luna/Terra 冻结组合。一次运行启动后 driver、provider、每项职责的模型和档位均被冻结，不能热切换。

Qwen3.8 Flash 的五类代表资格均通过；正式 500 题直接职责的保守投影为 `9,768.865 s < 20,400 s`。资格结果不替代正式 preflight 或完整评测。

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
