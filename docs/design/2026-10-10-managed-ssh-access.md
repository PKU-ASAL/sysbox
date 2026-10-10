# P7：受管 SSH access 的完整迁移

这是已批准 P7 的实施细化，不是完成声明。现有 CLI 和 supervisor 的
`CopyFile` / `rm -f` 路径没有内容版本，也没有共享 sshd 的配置闭环，最终必须删除。

## 设计

采用 guest helper 持久管理访问记录，sshd 通过固定的 `AuthorizedKeysCommand`
（OpenSSH `%u` token）读取已提交的公钥。原来的 `AuthorizedKeysFile` 保持由管理员管理，
Sysbox 不改写用户自己的文件，也不停止共享 sshd。OpenSSH 命令不提供本地监听地址
或端口 token。仅传 `%u` 时，helper 只返回没有 BindIP/Port 限制的配置；配置有端点限制
而实际端点未知时拒绝授权。管理员可给单一监听端点的 sshd 配置固定地址/端口参数，
多监听端点通过 `Match LocalAddress/LocalPort` 选择固定 helper 参数，先写地址与端口
交集，再写单独地址/端口，最后是 `Match all`。生成器合并重复端点、按稳定顺序输出，
限制规则交叉展开数量。参数 `-` 和 `0` 分别表示未知地址/端口，不放宽 key 限制。
此生成器已经双端口真实 sshd 验证；自动安装、第三方命令冲突检测、配置校验/reload
及其恢复仍未接入。helper 不是执行任意命令的入口。

生成文本必须放在经过验证的配置边界：已有 AuthorizedKeysCommand 或更早的 Match
规则可能抢先生效，后续全局指令是否合法由目标 OpenSSH 实际校验。自动安装时须检查完整配置
的 `sshd -t` 和各端点 `sshd -T -C`，不能把生成文本直接插入任意 Include 位置。

`ValidateSSHD` 已实现暂存完整配置的语法与生效检查，对每个显式连接探测核验
helper 命令、执行用户，以及必须保留的 AuthorizedKeysFile。真实 OpenSSH 测试覆盖
端点交集、部分匹配、无匹配和更早规则冲突。探测集合必须由部署 adapter 枚举相关
user/group/source/endpoint 条件；有限探测不是任意管理员配置的全局等价性证明。
此方法不写文件、不 reload，也不代表运行中的 daemon 已采用暂存配置。

生产 `NewSSHDRunner` 使用可信绝对路径直接执行 sshd，只接受 `-t/-T` 校验参数。
每次调用独立限制为 5 秒，stdout/stderr 总量限制 1 MiB，超限立即取消；不返回失败时
的部分 stdout，stderr 不参与有效配置解析。管道继承的额外等待上限为 100ms。
验证器的整个探测集合仍需调用方提供总 deadline，逐次超时不等于整体预算。

相比检查文件后执行 rm，这个模型将权限内容、CAS 版本、操作结果放在同一原子记录中，
避免检查后内容被替换的窗口。相比每项 access 启动独立 sshd，它不要求为每项访问维护
额外进程、端口和主机密钥。若已有第三方 AuthorizedKeysCommand，报告配置冲突，不覆盖。

一个 access 由受信任 node 用户名和 AccessID 定位，绑定 topology UID、node resource
UID/generation。创建期望版本为 0；轮换和删除必须给出当前版本。删除保留 tombstone，
版本不回退，旧请求不能删除或重新激活新配置。相同操作 ID/摘要有持久结果可只读恢复；
相同 ID 的不同载荷拒绝。不同 owner 不得通过同名 access 抢占记录。

公钥记录属于长期配置，不随授权这次配置修改的 grant 到期而自动消失。每次修改仍须
当前签名授权和 guest scope epoch；查询不产生修改授权。授权必须绑定 guest boot，
不能把 host boot 签名直接用于 VM guest。多后端 authority 和宿主停止回执继续复用现有
P2–P4 模型，不另建控制平面状态机。

## 实施顺序与验收

1. guest 原子记录与 CAS：新增 `pkg/substrate/ssh_access.go`，在
   `pkg/guesttask/ssh_access.go` 复用 guest 私有目录、scope 锁和持久化工具。
   用真实临时目录测试轮换、旧删除、其他 access 保留、并发 CAS、重开恢复、损坏拒绝。
   通过前不接入任何生产调用方。
2. guest 协议与 SSH/vsock SPI：接入 `pkg/guesttask/protocol.go`、`worker.go`、
   `pkg/transport`；sshd helper 查询接入 `cmd/sysbox-init/task.go`。记录与输出均不含私钥。
   测试未知版本拒绝、身份与摘要绑定、旧 epoch 拒绝、失去回报后只读恢复。
3. host/API/runtime：canonical node user 和当前 access 版本构造请求，向 guest 派发
   正确 boot 的授权；存储与返回版本化配置证据。删除原 CopyFile/rm 路径，观察必须
   查询当前配置，不能把历史成功回执当作当前存在。
4. 共享 sshd adapter：明确支持已安装 OpenSSH 的 guest；只写 Sysbox 专属 drop-in，
   检查有效配置、`sshd -t` 和监听状态，持久记录 reload 的结果。拒绝已有第三方
   AuthorizedKeysCommand 冲突；不执行发行版软件包猜测链。
5. 真实独立 sshd 验收：初始 key 可登录、轮换后旧 key 拒绝、新 key 可登录；删除后
   此 access 拒绝，另一个受管 access 与原 authorized_keys 登录均正常。再接双 VM
   API/Agent 重启、回执丢失和 CLI 入口验收。

每一步执行失败回归、实现、相关包 race、独立审查，再记录真实证据。第 1/2 步是
后续接线所需的内部能力，不能据此宣称 P7 已完成。网络能力矩阵仍是 P7 的另一个门槛。

## 响应丢失的恢复身份

SSH transport 在派发前由请求和签名 grant 计算完整 handle。派发后即使响应丢失或
载荷无效，返回 error 时仍保留这个预期 handle；此时 Present 不代表已确认结果。
调用方必须将 handle 持久保存并通过 Observe 查询原操作，不能因超时创建新操作。
派发前校验失败不返回 handle，响应中的陌生身份不能成为恢复目标。

已用真实 guest 原子记录验证创建/删除提交后丢响应、重开 Engine、只读查询原结果，
且 mutation 调用次数不增加。此测试不代表签名子授权派发、host/API completion 或
真实跨宿主丢包已完成。

## 无 handle 的只读发现

`ConnectionSSHAccess.DiscoverSSHAccess` 按 canonical node、topology UID、resource
UID/generation、user 和 AccessID 查找。guest 不接受 worker 节点名作为文件路径，
transport 先核对自身节点/代际，guest 再验证记录内 owner/代际。

返回 present/absent 或带错误的 unknown。从未记录的 access 没有 Current；已删除
access 返回 Current tombstone 及最后 revision，防止再次创建时回退到版本 0。持久
配置可跨 guest 重启存在，因此响应同时区分当前 BootID 与历史 handle 的 BootID。
配置摘要、完整身份与响应 query 均在 transport 验证；错误不泄漏外来记录。

发现不创建锁文件或记录、不改变授权，也不代表 sshd 配置已加载或监听正常。
真实 Docker helper 已验证首次缺失、创建后发现、外来 owner 拒绝及删除后版本 3；
SSH/vsock adapter 仍须各自真实验收，runtime/API 消费此入口尚待接入。

宿主 Unix RPC `ssh-discover` 只接受 TargetRef 与 AccessID；不接受用户名、provider ID
或配置路径。Dispatcher 使用 canonical resolver 的用户/连接/节点身份，复用当前
provider fencing 后读取配置。返回给 worker 的 NodeID 映射回 canonical 节点名，
不让 worker 用 provider 外部 ID 选择连接。缺少 canonical 用户明确拒绝，不推测 root。
`DiscoverManagedSSHAccess` 客户端再次核对 scope、AccessID 和配置摘要。
transport 与宿主共用 `SSHAccessDiscovery.Validate`，防止边界校验语义漂移。

独立 Unix socket 回归已验证实际请求/响应；测试 resolver 提供受信任 fixture，
不将它描述成 canonical repository、API 或跨宿主发现的完整验收。
CanonicalResolver 现从节点 state 或当前 Run candidate 的持久 desired 配置恢复用户：
优先第一项 connection.User，其次 provider_config.SSHUser；都没有则保持未指定。
仅在实际节点 identity 核验后构造 guest 连接目标的用户，不读取 worker 的用户名或
未发布 workspace，不补 root 默认值。真实 journal 序列化回归已验证候选冷读取保留用户。
没有持久声明用户的节点仍拒绝 SSH access；完整远端授权/自动配置部署尚未关闭。
用户解析不属于 provider-only 的 NodeTarget：SSH 配置损坏不会阻止电源操作解析；
需要 guest 连接时才校验，且在建立连接之前拒绝非法用户。

## Guest 修改的停止回执

复用 SignedExecutionStopReceipt，新增 `ssh_result`，绑定具体 committed handle 和
present/deleted 结果。`guest_ssh_access_apply/delete` 的通用停止签名缺少结果时拒绝。
专用签名/验证同时核对原 mutation 的摘要、用户、AccessID、ExpectedRevision+1、
guest boot、scope/generation 和 epoch；合法 host 签名不能替代 canonical 请求校验。
专用验证还要求调用方提供 canonical 节点名并精确匹配回执 NodeID；缺失或其他节点拒绝。

通用 stopped HTTP 入口拒绝 guest SSH 回执，避免 canonical SSH 完成提交之前释放
scope。NodeOperation 已保存不可变 SSHMutation 和专用完成流程提交的 SSHResult；
普通 CAS 不得注入/改变结果，worker completion 不能结束此操作。现有 node-operation
host completion HTTP 路由已核验签名/原请求/节点，再复用三存储适配器和共享 transition
提交结果后释放 scope。命令退休后复用保留的 operation 请求与 tombstone 命令身份；
相同回执重试不改变结果，后继 attempt、不同节点或 CAS revision 拒绝。

文件、SQLite 和专用真实 Postgres 的 HTTP 专项回归已执行通过。公开受理与宿主修改
派发仍待接入，不能据此宣称完整远端 SSH 生命周期已完成。

正式 execution-grant 入口现已识别 guest SSH node-operation，由持久 command 中的
SSHMutation 构造摘要，核对内外层 agent/action/resource/generation、操作类型和状态。
签发请求只能携 session、lease owner、attempt 和 guest boot；额外 mutation 字段拒绝。
在线授权复核同一 source，因此即使签名有效也不能替换原 CAS revision 的摘要。
命令退休、租约失效、旧 attempt 均不能重新签发。测试预置已受理 command，尚不证明
公开业务受理接口已能创建该 command。
当前 source 验证 command 内的 operation 快照；公开派发启用前，还需将独立
NodeOperation 的终态/撤销/替换检查纳入同一授权读写边界，不能以旧 queued 快照
长期替代当前操作状态。
共享 codec 同时拒绝 queued/running 预置 SSHResult，raw 创建与原子 dispatch 都执行
相同检查；普通 CAS 不得推进 guest SSH 终态。完成继续精确匹配 command attempt，
不同受理轮次不自动套用旧回执；生产重新投递语义仍需在派发接入时完整验证。
回执不能依据连接中断或当前资源不存在补造；必须由宿主确认原 guest 修改已提交。
