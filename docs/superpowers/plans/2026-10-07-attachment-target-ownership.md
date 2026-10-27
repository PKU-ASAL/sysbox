# Attachment target ownership implementation plan

**Goal:** 网卡连接必须验证目标网络代际和真实 bridge，不接受旧的仅名称记录。

**Architecture:** runtime 单独传递目标网络 OperationIdentity；network 共享层读取 v2 ownership anchor，并在 host lock 下固定 namespace FD、检查 bridge ifindex/type/alias。Docker veth 和 Firecracker TAP 在这个句柄上接桥。libvirt 保存目标身份并在 Start 再验证。

**Constraints:** 不兼容旧记录；不自动删除用户资源。Docker 原生 NAT 通过 daemon ID/labels 验证。Firecracker/libvirt 不猜测 Docker 的 Linux bridge 名称，这条未经证明的跨 provider 路径明确 unsupported。libvirt 最终由 daemon 按名称接桥，不能声称抵御非协作管理员在校验后的替换。

- [x] 测试：缺失身份、旧 anchor、错误网络代际及同名 bridge 替换拒绝；验证失败不创建 veth/TAP。
- [x] runtime 的 AttachmentRequest 增加 NetworkIdentity，在创建和恢复请求中从网络资源填入，不能用节点身份代替。
- [x] network 新增受锁保护的 bridge 使用域；校验 anchor v2、网络身份、proof、namespace 和内核对象，close 释放 FD/lock。
- [x] Docker 接入目标校验；veth 创建和接桥使用固定句柄；原生 NAT 在 connect 前用网络身份检查 daemon 标签。
- [x] Firecracker TAP 接入共享 bridge 使用域，移除 shell/name 接桥以及 NAT bridge 名称推导。
- [x] libvirt 在 attachment 中保存网络身份并在 Start 前再次校验；旧记录要求重建。
- [x] 运行网络/TAP 真机隔离 E2E、相关包 race、全量回归；独立审查并修复；更新状态和接口文档。

每项先增加可复现的失败测试，再实现与验证。测试命令使用 GOCACHE=/tmp/sysbox-gocache；真实资源测试仅清理测试创建的对象。

审查补充已落实：root bridge 同时验证命名 namespace；Firecracker manifest v2 保存每条 TAP 证明，启动核对配置与真实 master；libvirt 对已有 domain 的 inactive XML 做完整接口集合校验。真实 KVM 生命周期/crash/reset/冷恢复、真实 libvirt XML 漂移拒绝通过。旧测试夹具对瞬时 /proc 快照的假设改为有界等待，生产 unknown 语义未放宽；两项故障测试连续 50 轮通过。
