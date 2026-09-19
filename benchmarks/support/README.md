# 测试支持与外部智能边界

Ownward维护通用外部智能端口、测试编排、任务提示、工具预算、评分、恢复和证据身份。供应商客户端、认证、服务切换、供应商专用配置与测试由仓库外工具维护；新增或切换供应商不向产品仓库加入专用实现。

## 接入

运行器从 `OWNWARD_EXTERNAL_INTELLIGENCE_SELECTION` 指定的外部清单加载适配器；未设置时读取 `~/.config/ownward/external-intelligence-runtime.json`。未安装时可以读取帮助和执行不依赖模型的检查，实际调用前明确报错，不回退到其他服务。清单是可信的本机执行配置，其中的适配器是会执行的Python代码。

清单沿用 `ownward.external-intelligence-selection/v2`，包含默认driver、实现元数据、各职责的模型与档位，以及 `adapters` 映射。适配器路径可以相对清单或为绝对路径；执行配置保留 `external_intelligence` 的 `driver`、`binary`、`credential_file` 和可选 `roles`。

项目通过 `ExternalIntelligenceTransport` 接收结构化输出、动态工具调用、用量与失败。宿主必须遵守原Schema、权限、预算、时限和恢复约定；代码不决定答案，不允许以切换供应商为由放宽判分。

## 证据与复用

运行身份绑定所选供应商、模型档位、外部适配器源码和实际制品。外部文件按独立路径标识及内容摘要进入证据，不要求源码位于仓库，也不因无关供应商变更而失效。旧记录保持原身份；新运行不得伪装成历史版本或直接拼接不同条件的成绩。

纯供应商迁移不修改原文、内核检索、语义组织或信息使用策略。产品公共连接器修复按自身职责维护，与是否安装某家外部工具无关。

## 离线检查

`fixtures/external-intelligence-runtime.json` 是保留历史driver与角色字段的合同测试夹具；它只加载 `fixtures/fake_adapter.py`，没有模型调用、认证或网络能力，不能用于真实测试。CI通过环境变量显式选择该夹具，不安装任何供应商客户端。

本机工具安装与使用说明位于仓库外 `E:/Dev/external-intelligence/README.md`；该路径是当前安装位置，不是产品依赖。
