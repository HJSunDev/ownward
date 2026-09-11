# OKF：知识的交付、交换与核验

调研日期：2026-09-09；核对版本：v0.2。依据公开规范、参考实现说明及社区反馈，未做本地性能实测。

## 核心定位

**Open Knowledge Format（OKF）约定如何把知识内容及其必要说明保存为人和智能体都能读取、跨系统携带的文件。** GoogleCloudPlatform 仓库以格式规范为核心贡献，另提供生成知识包的智能体和可视化工具作为概念验证；生产和读取不绑定特定模型、框架或服务。[项目说明](https://github.com/GoogleCloudPlatform/open-knowledge-format)

## 知识包里有什么，怎样使用

一个知识包就是一组 Markdown 文件及其目录；正文保存知识，文件头的 YAML 字段保存可供程序读取的说明。

| 部分 | 保存什么、有什么作用 |
| --- | --- |
| 知识文档 | 正文可包含概念说明、数据结构、经验和示例；`type` 标明类型，是普通文档唯一始终必填的字段。 |
| 关联与导航 | 普通链接连接相关知识，目录索引帮助读取方先了解范围，再按需打开内容。 |
| 来源与可信线索 | 记录来源、生成者和核验者，区分“谁写的”与“谁确认过”。 |
| 生命周期 | 记录草稿、可用或弃用状态，以及预定过期时间，供读取方判断是否需要复核。 |

字段要求见[规范](https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md)，导航与使用方式见[项目说明](https://github.com/GoogleCloudPlatform/open-knowledge-format#why-okf)。

**流转过程：生产方整理内容并写入说明 → 打包或同步文件 → 读取方浏览索引、读取相关文档和来源 → 按自己的任务使用。** 生产方可以是人、脚本或智能体；维护者更新文件后，可通过 Git 比较变化。普通读取不要求调用 LLM。

仓库示例先从 BigQuery 元数据生成文档，再由 LLM 查阅指定网站、补充相关知识；网站补充可关闭。这只是生成 OKF 的一种方式，并非所有使用方必须执行的流程。[参考智能体](https://github.com/GoogleCloudPlatform/open-knowledge-format#how-the-reference-agent-works)

## 特殊能力：计算结果也可携带核验约定

v0.2 的 **Attested Computation** 可以一起描述计算方法、可填参数、执行方式和核验程序。例如计算年度收入时，智能体填写年份，执行方运行约定查询并返回记录，核验程序检查实际查询及交付数值是否一致。

**OKF 保存约定，接入方负责执行与核验。** 定义经过审核和某次执行符合定义是两件事；按指定方法算对，也不能证明原数据或业务定义正确。完整运行协议、核验器可移植性及沙箱约定仍列为后续工作。[计算核验与边界](https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md#10-attested-computations-concept)

## 成本与局限

**格式本身没有统一耗时。** 文件读取之外，知识生成、持续更新、模型理解及计算核验是否发生、花费多少，取决于生产与消费系统；参考流程包含模型请求和数据库查询成本，公开说明未提供可用于判断整体提速的对照数据。[参考流程与费用说明](https://github.com/GoogleCloudPlatform/open-knowledge-format#credentials)

| 局限 | 对使用方意味着什么 |
| --- | --- |
| 标记提供线索，不保证内容正确 | 来源和核验字段是可选记录；符合格式并不意味着内容已经核实，读取方仍须判断依据与时效。[规范](https://github.com/GoogleCloudPlatform/open-knowledge-format/blob/main/SPEC.md#5-provenance-trust-and-lifecycle) |
| 关系含义仍依赖正文表达 | 实现者反馈，不同工具表达关系类型、来源和可信程度的方式不同，交换时可能丢失含义；结构化关系扩展仍是开放提案。[社区反馈 #16](https://github.com/GoogleCloudPlatform/open-knowledge-format/issues/16) |
| 跨系统核验归属仍有缺口 | 实现者反馈，导入再导出时难以明确区分“上游核验过”与“本地重新核验过”；相关字段尚属提案。[社区反馈 #15](https://github.com/GoogleCloudPlatform/open-knowledge-format/issues/15) |

以上社区反馈反映具体接入经验，不能视为已落地能力。文件易于交换的收益，需要与生成、更新、纠错及接入成本分别评估；采用同一种格式本身不保证质量提高或等待缩短。

## 对 Ownward 的意义

**借鉴内容连同来源、核验和时效说明共同传递的约定，正式格式兼容后置，不列为第一版发布前工作包。** 截至2026-09-10，已有[Google 产品接入](https://cloud.google.com/blog/products/data-analytics/scale-okf-bundles-across-an-organization-with-knowledge-catalog)与社区实现，但尚不足以认定行业普遍采用，结构化关系交换仍有开放提案。未来按实际互操作需求在导入导出边界适配，保持身份与语义保真，不改换内核；收益不足以覆盖转换、存储和维护成本则不采用。
