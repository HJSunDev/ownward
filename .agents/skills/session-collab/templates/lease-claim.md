# 资源租约声明模板（collab/resources/claims/<lease_id>.md）

```yaml
lease_id: <id>；role: <角色slug>；action: <动作>；class: heavy|external|workspace-write
estimate: <内存/CPU/时长/费用估算>；preemptible: true|false；safe_point: <描述>
granted_at: <时间>；expires_at: <时间>；heartbeat: <时间>
status: running|released|expired|preempted
actual: <实际用量>；result: <结果摘要>
```

规则：
- 先原子创建槽目录（`resources/slots/<class>/slot-1`）再写声明；占用则写 `claims/pending/`，下一自然回合重试。
- `expires_at` 覆盖最大预期运行时间；长任务拆检查点续期。
- 回收：仅当持有者空闲且已过期，原子重命名入 `claims/expired/`；busy 不回收。
- 抢占：协作式——记录理由并通知；确认持有者空闲后才接管；仍在运行则记录违规并升级用户。
- 完成：声明移入 `claims/done/`，删除槽目录。
