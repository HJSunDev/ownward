# 角色文件模板（collab/roles/<role-slug>.md，一角色一文件，角色本人维护）

```markdown
# <角色名>
- 角色 slug：<role-slug>
- 当前会话：<名称>（<会话ID>）
- 职责边界：<该角色负责什么、不负责什么>
- 状态：idle|working|waiting
- open_threads：
  - <thread-id>：<phase>，等 <角色>
```

规则：逻辑角色是耐久身份，会话 ID 只是本地映射（迁移只更新映射）；每回合只核对自己名下的线程；状态变化时同步更新 open_threads。
