# research 方法来源

供修改本技能时查阅，调研时不需要读。这里只记方法依据；设计与验收基准见 [requirements.md](requirements.md)，需求缘起与边界见 [background.md](background.md)。下表来源都已实际核读。

| 来源 | 采用的机制 | 未采用的范围 |
|---|---|---|
| [Heuer《Psychology of Intelligence Analysis》](https://www.cia.gov/resources/csi/static/Pyschology-of-Intelligence-Analysis.pdf)（第 4、8 章） | 防“满意即止”（第一个看起来够好的答案就停）；区分没有证据与有反面证据，先判断证据本来能否被看到；想清“若成立本应出现却没看到的东西”；比较有区分力的证据与竞争解释；理论帮助抓关键，历史类比需相关方面可比 | 完整的竞争假设矩阵程序；把书中针对欺骗情境的“系统搜索后仍无证据才可排除”（排除前的最低门槛）泛化为排除的充分条件；把机构经验当作普遍准确性保证 |
| [Anthropic《How we built our multi-agent research system》](https://www.anthropic.com/engineering/multi-agent-research-system) | 先宽后窄；投入随问题复杂度伸缩；优先一手来源，警惕 SEO 内容；信息可能只在特定渠道；引用要与主张相符 | 多智能体编排与并行调度 |
| [Cochrane Handbook 第 4 章](https://www.cochrane.org/authors/handbooks-and-manuals/handbook/current/chapter-04) | 单一来源不足会带来偏差；多渠道、灰色文献、追参考文献与联系知情者补漏；同一研究的多份报告不是独立证据 | 临床数据库清单、PICO、固定更新周期 |
| [Caulfield《SIFT (The Four Moves)》](https://hapgood.us/2019/06/19/sift-the-four-moves/) 与 [Wineburg、McGrew 横向阅读研究](https://ed.stanford.edu/news/stanford-scholars-observe-experts-see-how-they-evaluate-credibility-information-online) | 先辨来源与主张；到别处看对来源的评价；找更好的覆盖；把主张追回原始语境 | 面向新闻信息素养的教学流程 |
| [Gentner 1983《Structure-Mapping》](https://groups.psych.northwestern.edu/gentner/papers/Gentner83.2b.pdf) | 结构类比映射的是关系结构而非表面属性，按系统性选择要映射的关系；因此采用结构类比时说明对应关系与失效差异 | 类比理论本身的计算模型；把结构映射当作所有跨领域借鉴的唯一原则 |
