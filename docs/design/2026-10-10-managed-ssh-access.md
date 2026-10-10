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

生成文本必须放在经过验证的配置边界：`Match all` 不会恢复全局配置上下文，已有
AuthorizedKeysCommand 或更早的 Match 规则可能抢先生效。自动安装时须检查完整配置
的 `sshd -t` 和各端点 `sshd -T -C`，不能把生成文本直接插入任意 Include 位置。

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
