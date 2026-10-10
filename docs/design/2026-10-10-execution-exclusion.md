# 生命周期执行互斥

目标：同一 repository/topology 的协作执行者在读取状态前取得执行锁，
直到 apply、reset、destroy 或节点电源操作返回后释放。等待可取消，失败不得执行。
这属于 P0–P9 恢复工作的一部分，不代表 continuation 或分布式 fencing 完成。

进程内 mutex 无法覆盖独立 CLI；直接持有 aggregate 事务锁会阻塞执行过程自身的
action checkpoint。采用独立执行锁，并保留短时 aggregate 事务锁。

- 文件：repository 目录下独立 execution lock，保持 inode，不在释放时删除。
- SQLite：数据库旁的独立 execution lock，按 topology 隔离，不占数据库连接。
- Postgres：独立连接持有实际 aggregate 表 OID/topology 范围 advisory lock，不占 repository
  事务连接池。数据库重启或连接中断会释放锁，不能将其当作 provider fencing。
- CLI 和 Agent：锁失败直接返回；先锁后读；defer 释放。API 原有进程内协调锁
  仍用于与同进程的管理入口协作。

执行锁只排除仍持有锁的协作进程。它不证明旧二进制退出、daemon 请求完成，
也不替代远端执行授权、watchdog 或未知结果观察。显式 reset continuation
仍需验证原完整计划、Run incarnation、旧执行者退出及 effect 状态。

SQLite 解析数据库和父目录软链接，确保数据库与锁使用同一规范路径。
硬链接、不同挂载视图及运行中的路径替换不在当前路径规范化保证范围内。
所有执行者必须使用稳定的同一数据库路径；禁止通过这些别名并发操作。
API 管理入口目前仍使用原有进程内协调机制，不能将本次改动理解为所有
管理写入都已受跨进程执行锁保护。

实施及验收：

- [x] repository 提供 `LockExecution(context.Context, string) (func(), error)`。
  测试同 topology 超时、其他 topology 独立、释放重取、锁内 state 事务可用。
- [x] 用子进程证明文件/SQLite 跨进程互斥，真实 Postgres 验证独立连接互斥。
- [x] Bridge 锁契约携带 context/error，所有生命周期调用在锁失败时停止。
- [x] CLI/Agent/API race 回归和独立审查；记录数据库断连语义，继续恢复主线。
