# 资源租约声明

使用共享工作区的资源槽时，将声明保存为 `collab/resources/claims/<lease_id>.md`。获取、续期、回收与抢占按[资源协调规则](../../../../docs/workbench/cross-session-collaboration.md#四资源协调)执行。

```yaml
lease_id: <唯一ID>
role: <持有角色>
action: <动作与目标>
class: heavy|external|workspace-write
estimate: <CPU、内存、时长或费用估算>
preemptible: true|false
safe_point: <可安全交接或终止的位置>
granted_at: <时间>
expires_at: <时间>
heartbeat: <最近确认仍在执行的时间>
status: running|released|expired|preempted
actual: <实际用量，完成时填写>
result: <结果摘要，完成时填写>
```

确认独占获取成功后才能执行；槽已存在不算获取成功。记录持有者与 `lease_id`，释放或回收前核对身份。完成后更新声明、移入 `claims/done/`，再释放本人持有的槽；不得清除其他角色的有效租约。
