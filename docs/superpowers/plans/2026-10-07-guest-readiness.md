# Guest readiness barrier

**Goal:** 让 Firecracker guest 的副作用等待基础初始化完成，避免启动期间 tmpfiles 删除传输暂存文件。

**Contract:** `ping` 只返回 RPC liveness；`ready.v1` 返回 `Ready` 和 `ReadinessReason`。Exec 与 write_file 在 Ready=false 时拒绝，console 仍允许诊断。systemd 要求 PID1 已交接、`sysinit.target` active、`systemd-tmpfiles-setup.service` active 且 Result=success；自定义 init 必须写入匹配当前 `/proc/sys/kernel/random/boot_id` 的 `/run/sysbox/boot-ready`。

真实 KVM 延迟 tmpfiles 的回归确认 readiness 前无 Exec/Copy effect，初始化后连续文件安装成功。旧 guest-init 不兼容，必须随 host transport 一起重建 VM。
