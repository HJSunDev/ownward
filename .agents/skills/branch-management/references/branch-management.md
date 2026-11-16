# 研发、发布与持续维护

## 一、需求

这些需求面向我们当前及未来的不同软件项目。当前智能体项目仅用于举例，它的业务特点和发布阶段不限定需求的适用范围。它目前尚未发布、需要尽快发布；这些需求同样适用于已经发布的项目，以及后续长期的开发和维护。

### 核心需求

1. **稳定的线上版本：** 提供并维持稳定可用的线上版本，保障用户正常使用。
2. **紧急修复：** 以保障线上稳定运行为目标。线上出现紧急问题时，能够迅速修复并更新线上版本，恢复或保持正常运行。
3. **持续开发：** 面向新的功能迭代和架构升级，持续推进系统演进；开发过程不影响线上稳定版本。
4. **长期维护：** 以主动挖掘和解决系统已有缺陷为核心，能够随时启动自动化的问题发现、处理与优化工作；这项工作能力需要长期可用。

## 二、代码分支管理的核心原则

涵盖代码分支的使用与维护，以及与其直接相关的软件版本维护和应用发布。

这 10 条由双方各自从一手资料中独立搜集、优选，再经交叉质询形成。中文表述依据原始资料凝练，出处附在每条之后。

1. **先确立权威的集成位置，再按真正不同的准入政策，保留必要的长期代码线。**
   - 指导：新增一条长期线，要有真实、独立的准入需求，并评估分叉和集成的成本，不按任务类别机械分配。短期任务分支不受此限。
   - 出处：[Wingerd & Seiwald 1998](https://it323.wordpress.com/wp-content/uploads/2011/12/perforce-best-practices.pdf)“Have a mainline”“Branch on incompatible policy”；[Software Engineering at Google 第 16 章](https://abseil.io/resources/swe-book/html/ch16.html)“Source of Truth”。

2. **小批量、及时地集成，每次合入都是完整、可验证的一步，不把大合并留到交付末期。**
   - 指导：一项工作迟迟合不进去，就先拆小，不让分支长期漂移。
   - 出处：[Fowler《Continuous Integration》](https://martinfowler.com/articles/continuousIntegration.html)；[DORA](https://dora.dev/capabilities/trunk-based-development/)；[Brindescu 等 2020](https://www.ics.uci.edu/~iftekha/pdf/J4.pdf)。

3. **以实际目标状态和有效证据，把关合入、移植与发布，保持公共基线健康；输入变化后，重新核实证据是否仍然适用。**
   - 指导：验证的是合并或挑拣之后的真实版本。公共基线一旦不能通过既定的构建或测试关卡，优先恢复它，暂停无关的改动继续合入。
   - 出处：[GitLab merged results pipelines](https://docs.gitlab.com/ci/pipelines/merged_results_pipelines/)；[GitHub merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)；[Fowler《Continuous Integration》](https://martinfowler.com/articles/continuousIntegration.html)“Fix Broken Builds Immediately”。

4. **跨多次集成的大改动，优先拆成可验证的渐进步骤；必要的隔离机制，要有明确的边界和退出条件。**
   - 指导：大功能或架构升级，先找能分步合入的切法，比如抽象层或开关。实在拆不动，才开隔离分支，并事先定好同步方式和退出条件。
   - 出处：[Branch by Abstraction](https://martinfowler.com/bliki/BranchByAbstraction.html)；[Feature Toggles](https://martinfowler.com/articles/feature-toggles.html)；[Fowler《Patterns for Managing Source Code Branches》](https://martinfowler.com/articles/branching-patterns.html)，Future Branch 一节。

5. **用固定、可追溯、可重建的完整基线发行软件；已发布版本不原地改写，内容修改另发新版本。**
   - 指导：线上运行的是什么，以版本标识和制品为准；任何发布都要能准确定位、复现和重建。这一条不妨碍为了恢复服务，重新部署一个已验证的旧版本。
   - 出处：[SemVer 2.0.0 第 3 条](https://semver.org/)；[SWEBOK 软件配置管理](https://swebokwiki.org/Chapter_6:_Software_Configuration_Management)，Baseline 一节；[Google SRE 第 8 章](https://sre.google/sre-book/release-engineering/)。

6. **稳定维护中的改动，按兼容义务、实际收益和回归风险核准，不夹带未经接受的演进。**
   - 指导：决定什么能进入稳定系列的维护更新。修复本身需要的结构调整可以核准；与本次维护无关的演进不进入。
   - 出处：[Python 开发周期](https://devguide.python.org/developer-workflow/development-cycle/)；[Linux 稳定版规则](https://www.kernel.org/doc/html/latest/process/stable-kernel-rules.html)；[Kubernetes cherry-pick 流程](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-release/cherry-picks.md)。

7. **修复要传播到所有适用的受支持版本和后续开发目标，逐个验证，并记录落实状态。**
   - 指导：先确定哪些目标适用，再逐个核实是已经修好，还是确实不适用。紧急补丁可以先上线恢复服务，但其余目标补齐之前，这项修复不算结束。
   - 出处：[Wingerd & Seiwald 1998](https://it323.wordpress.com/wp-content/uploads/2011/12/perforce-best-practices.pdf)“Track change package propagations”；[Linux 稳定版规则](https://www.kernel.org/doc/html/latest/process/stable-kernel-rules.html)；[Microsoft Release Flow](https://learn.microsoft.com/en-us/devops/develop/how-microsoft-develops-devops)。

8. **保护用户已有的正常可用行为；发生回归时优先恢复，并验证恢复动作本身是否兼容。**
   - 指导：按实际影响，优先恢复受损的正常使用。回退或回滚之前，先核实新旧版本之间的数据和协议是否兼容。
   - 出处：[Linux 回归处理规则](https://www.kernel.org/doc/html/latest/process/handling-regressions.html)；[AWS《Ensuring rollback safety during deployments》](https://builder.aws.com/content/3F04j2yRAAMBuPSPs50xwXZqg01/ensuring-rollback-safety-during-deployments)。

9. **分支从创建起就明确责任人、去向和结束条件；退役前，确认成果已经集成、妥善保留，或者明确放弃。**
   - 指导：清理分支之前，先确认不会丢掉尚未交付的工作。
   - 出处：[Wingerd & Seiwald 1998](https://it323.wordpress.com/wp-content/uploads/2011/12/perforce-best-practices.pdf)“Give everything an owner”；[Fowler《Patterns for Managing Source Code Branches》](https://martinfowler.com/articles/branching-patterns.html)；[Pro Git 分支管理](https://git-scm.com/book/en/v2/Git-Branching-Branch-Management)。

10. **把开发、验证与生产各自的可变状态和写入影响隔离开；代码分支并不等于运行环境隔离。**
    - 指导：几项工作并行时，会被写入的构建产物、数据目录和运行实例各自分开；只读的工具和不可变的制品可以共用。
    - 出处：[Wingerd & Seiwald 1998](https://it323.wordpress.com/wp-content/uploads/2011/12/perforce-best-practices.pdf)“Segregate built objects from original source”“Don't share workspaces”；[SLSA v1.2 Build L3](https://slsa.dev/spec/v1.2/build-requirements)；[Fowler《Continuous Integration》](https://martinfowler.com/articles/continuousIntegration.html)“Test in a Clone of the Production Environment”。

### 适用边界

- “主线”“代码线”指的是职责，不规定分支叫什么、有几条，也不规定用合并还是 cherry-pick、修复先落在哪条线上。
- 第 8 条保护的是支持和兼容范围内的正常使用：既不把漏洞行为永久固定下来，也不承诺有状态的系统能无条件回滚。经批准调整兼容或支持边界时，要明确说明。

## 三、分支管理落地方案

### 两条常驻分支

- **main：最新正式版。** 普通开发不直接进入。一次正式发布或补丁经过验证、确认发布成功之后，main 才前进到这个版本；每次前进都对应固定的版本标签和发布包。
- **develop：下一版的集成线。** 汇集已经完成并验证过的功能、架构改动和日常修复。

### 具体流向

| 工作 | 从哪里开始、最终到哪里 |
|---|---|
| 新功能、架构升级 | develop → feature/具体任务 → 验收 → develop |
| 日常维护修复 | 随下一版交付的：develop → fix/具体问题 → develop；需要单独更新正式版的，走补丁路径 |
| 线上补丁、紧急修复 | main → hotfix/具体问题 → 验证并发布补丁 → main；随后把修复同步到 develop 和正在准备的相关发布候选 |
| 正常发布新版本 | 从 develop 选定待发代码，准备发布候选；需要专属修改时，建立 release/版本。验收并发布成功后更新 main，候选期间的修正同步回 develop |

### 版本关系

- 合入之前，先同步目标的最新变化、处理冲突，再验证合并后的实际代码。验证之后基线又变了，就补验受影响的部分。
- 同一系列的发布依次进行。发布没有确认成功，main 就不前进；合并不等于上线成功。
- 紧急补丁可以先恢复线上；但只要相关的开发线和候选还没补上这项修复，任务就不能结束。

### 长期自动化维护

不额外维护一条永久代码分支。长期保留的是工作台、问题记录和排查工具。每一轮在独立的工作区检查一个固定版本，具体修复按上表的路径进行。暂停的代码保留；完成并确认成果归属之后，才删除工作分支。

### 运行隔离

正式版与开发版分开安装、分开运行。日常使用固定的发布包，不能链接开发目录，否则一次开发构建就可能改掉正在使用的程序。
