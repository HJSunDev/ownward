# critical-task-record 规则来源

供修改本技能时查阅，执行记录时不需要读。下表来源都已实际核读。

| 来源 | 采用的机制 | 未采用的范围 |
|---|---|---|
| [Google SRE《Managing Incidents》](https://sre.google/sre-book/managing-incidents/) | 任务进行中持续维护一份记录；最重要的信息放在前面；失败的尝试也写进记录 | 事件指挥分工、多人同时编辑 |
| [OpenAI《ExecPlans》](https://developers.openai.com/cookbook/articles/codex_exec_plans) | 当前状态始终反映实际进度；取舍当时写选择与理由；意外附简短证据；只凭记录就能接续 | 按类型分节：单份临时记录里，取舍放在时间线上前后尝试与证据的旁边，读者不必跨节查找。先写完整计划再执行、结尾的经验总结，也不采用 |
| [Anthropic《Effective harnesses for long-running agents》](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents) | 接手时先读进度并核对实际状态；不把未确认的事当成已完成 | 用 JSON 功能清单和 git 提交作为进度载体 |
| [Allspaw《The Infinite Hows》](https://www.oreilly.com/radar/the-infinite-hows/) | 记录当时的判断依据，追问“怎么发生的”；允许多因和待查，不硬找单一根因 | — |
| [Nygard《Documenting Architecture Decisions》](https://www.cognitect.com/blog/2011/11/15/documenting-architecture-decisions) | 取舍写背景与全部后果；用中性语言记录事实 | 每个决策单独成文、带状态编号 |
