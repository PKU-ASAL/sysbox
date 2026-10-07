# P0–P9 并行实施：集成跟踪

当前已提交集成基线：`566268e`。本文记录并行集成检查点，不能作为P0–P9全部验收通过声明。

| 阶段 | 已进入代码的内容 | 未完成的验收/接入 |
|---|---|---|
| P0 | 强制按Go依赖重建guest helper、amd64/arm64构建、独立cgroup验收目标 | 全能力矩阵、最终产物与总验收 |
| P1 | authenticated revision、独立Agent物化、artifact来源约束、续租固定快照 | 真正双宿主验收；未把独立目录当双宿主 |
| P2 | authority ledger/session/epoch、签名grant及stop receipt API、当前command事务绑定；guest task接入独立host supervisor、可信停止回执与旧epoch重试墓碑 | 普通Run/lifecycle/file/foreground等effect全部接入、host持有唯一provider权限；真正跨宿主验收 |
| P3 | 持久task、sealed memfd、cgroup+pidfd、monotonic guard、续租、分页输出、SSH/vsock通用SPI；真实SSH和Docker supervisor任务接续验收 | 完整Agent服务重启接管验收；全执行路径统一隔离 |
| P4 | session+sequence observation ledger、guest活动枚举、receipt失效 | 全API迁移回归、完整lease撤销/guest取消/hoststop集成 |
| P5 | adoption SPI、journal动作、Docker不可变ID sidecar | API plan/confirm、libvirt/Firecracker adapters、真实接管与清理 |
| P6 | aggregate format2快照、元数据restore monotonic serial、身份约束、API CRUD | grant撤销与freeze、force-unlock、恢复较旧资源集合的显式reconcile |
| P7 | managed Docker bridge导出/核验、FC/libvirt NIC消费 | 现有网络依赖自动重接、混合netns单VM支持边界；SSH服务生命周期与runtime。未接入的SSH配置草稿已删除，不计为实现 |
| P8 | 依赖合同已明确 | 自动GC尚未实施，不算完成 |
| P9 | 分项真实Docker bridge/TAP、private cgroup、KVM测试 | 双宿主、全故障矩阵、升级/main整合，不算完成 |

## 当前收敛规则

停止继续扩散接口，优先恢复全仓绿色并完成独立审查，再形成检查点。测试fixture迁移必须显式创建session/sequence，不得让生产代码兼容缺失证明。所有失败保留原因；真实环境缺失和skip不算通过。

## 首批新增验收

- 单独API与Agent目录的真实Docker完整Run、回执丢失与Destroy测试已运行；仍在同一宿主。
- Docker managed bridge + TAP证明、旧代拒绝与同名替换保护已运行。
- private cgroup任务测试覆盖去重、冷读取、进程树取消、supervisor退出、expiry、renewal、输出限制和Linux5.10 freeze/pidfd路径。
- KVM5.10 guest任务已实测启动/去重/退出结果/输出及异步取消确认；继续验收payload隔离和真实SSH helper。
- API全量测试在observation ledger迁移后曾失败；已经修复生产读写路径与测试fixture，最新启用真实临时Postgres的`go test ./...`全仓通过。
- 真实SSH独立sshd/私有cgroup E2E通过；当前guest在KVM Linux5.10上的持久任务E2E通过。任务以签名指定的非root UID执行，默认65534、NoNewPrivs与零有效capability；不允许任意root payload绕过supervisor。
- 复审修复了renew与旧超时竞争、Cancel在副作用前完整身份校验、不同resource的epoch互相误拒绝，以及snapshot历史属性不能盲目还原真实effect目标。

schema变更：API8、aggregate2；旧格式明确重建。它们必须在最终capability和配置样例中一起更新后才能交付。

## 独立 host supervisor 集成

API guest execution 不再由 Agent 自行打开 provider connection：缺少 supervisor
socket 会直接失败。新增 `sysbox host-supervisor`，独立校验 canonical resource
和在线授权，持久化启动意图，并在确认 guest 停止后签名回执。签名 grant 固定
guest boot ID；历史 grant 检查、旧 session 撤销和停止回执重放均有明确接口。

配置、身份、密钥、socket、任务日志及文件/SQLite canonical repository 必须满足
可信目录及权限要求。Postgres 的角色隔离仍需部署者配置。这不是整个 Agent 的
权限隔离完成声明：普通 Run、生命周期、文件安装和 foreground 路径仍待迁入。

可重复验收入口：`make test-host-supervisor-e2e`。使用真实 Docker task、Unix
socket 和私有 cgroup，覆盖停止回执丢失、API 与 supervisor 重建后重报、Agent session
替换导致运行中任务自动取消，以及确认停止后的后继 epoch。这里的服务重建是
测试内重新创建服务对象，不等同于双宿主或完整 Agent 进程重启验收。

本轮已修复 Agent 单次输出读取超过 guest 64 KiB 协议上限的问题，以及 supervisor
listener 异常退出时等待 recovery goroutine 导致挂起的问题。集成全仓测试使用
真实隔离 Postgres 通过。独立审查发现的旧授权结果误取消并发续租任务、慢记录
阻塞后续回执恢复均已修复并通过 race 回归：取消前重新比对完整 grant，已签回执
不再依赖在线授权，每条记录拥有独立超时。复审未发现新的阻断问题。

2026-10-07集成检查点：真实Postgres启用的全仓测试通过；API/persistence/Agent/runtime/guesttask/effectgate/transport/四provider相关race通过；vet与docs检查通过。最终重建amd64/arm64 helper后，真实KVM5.10持久任务去重、冷读取、输出、取消确认通过（10.04秒）。这证明当前基础可集成，不代表尚未挂接的host supervisor、P5/P6/P7功能和P8/P9已经完成。
