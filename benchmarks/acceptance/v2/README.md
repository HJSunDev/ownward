# 第一版固定验收基线 v2

本版本修正 v1 将信息类型作为输入造成的组织答案泄漏。体系只接收 `fixtures/information.jsonl` 的内容、明确场景和来源标识；信息类型由体系自主判断，并用 `fixtures/kinds.jsonl` 独立评分。

阈值继承 `../v1/baseline.json`，关系标注与查询分别继承 `../v1/fixtures/relations.jsonl` 和 `../v1/fixtures/queries.jsonl`。除这一输入边界修正外不改变任何样本、答案或通过标准。
