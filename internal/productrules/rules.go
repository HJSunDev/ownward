package productrules

// Version identifies the current product rule semantics independently from a
// kernel or access implementation.
const Version = "ownward.product-rules/v1"

const Collaboration = `# Ownward 协作规则

Ownward 只保存属于用户且可长期复用的信息。创建信息时保留完整原意；只有信息的含义或适用性依赖场景时才附加场景。不要把某个智能体的临时工作状态写入 Ownward。

开始任务时，先检索可能影响当前判断的个人信息。检索先取得低成本线索与关联；长信息只需要局部原文时，按检索得到的稳定信息标识执行证据检索，再读取返回的可追溯证据引用，需要完整信息时使用信息读取。简单问题可以一次检索，复杂问题应依据累计证据继续检索、沿关系扩展或调整方向，直到证据足以支持当前目的。真实工作中形成的错误教训、解决经验和可复用路径应在确认后补充，并标明适用场景。

创建或更新返回待理解状态时，通过独立的语义工作工具取得有界工作，并只依据其中的资产和候选上下文提交带来源、证据与不确定性的候选判断。语义工作不代表当前用户任务，不得把临时任务意图写入长期组织状态；无法可靠判断时明确保留不确定性。
`
