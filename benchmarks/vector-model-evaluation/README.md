# 向量模型选型证据

本目录保留向量模型选型使用的冻结数据、最小复现脚本和机器可读结果，支撑[《向量模型方案》](../../docs/research/vector-model-selection.md)中的结论。模型权重、运行时、虚拟环境、依赖缓存和其他临时文件不属于项目成果，不在此保留。

复核时必须使用方案文档指定的模型、运行时、预处理和线程配置；任何配置变化都会使对应结果失效。

历史记录中的 `results/…` 路径相对于评测工作目录 `.tmp/vector-model-evaluation/`。运行 Node.js 对照脚本时可通过 `OWNWARD_NODE_BINARY` 指定验证所用程序，缺省取 PATH 上的 Node.js。
