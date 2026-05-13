   跨主机恶意行为追踪：标注方法系统调研

   一、核心挑战：为什么标注很难

   学术界把这个问题叫 "dependency explosion"（依赖爆炸）——即使你知道哪个节点被攻击，从那个节点往上溯源，因果链会指数级膨胀，把大量正常行为也拉进来。

     攻击者写了一个 webshell 到 /var/www/html/shell.php
          ↑
     nginx 写了这个文件
          ↑
     nginx 父进程是 systemd
          ↑
     systemd 启动时读了 /etc/systemd/...（几百个文件）

   溯源链条上 99% 都是 benign 事件，真正的攻击节点被淹没。

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   二、学术界的三条标注路径

   路径 A：DARPA TC 方式——红队执行 + 事后人工标记

   这是目前最权威的 ground truth 产生方式，用于 DARPA Transparent Computing 项目（E3/E5 数据集）：

     1. 红队按剧本执行攻击（APT 场景，有时间轴记录）
     2. 蓝队同时收集全量系统日志（CamFlow/AuditD）
     3. 事后：红队提交 "Ground Truth Report"，列出：
           - 攻击开始/结束时间戳
           - 涉及的主机 IP
           - 关键事件（进程名、文件路径、网络连接）
     4. 研究者用这份报告，在日志里反查出对应节点，打标签

   优点：标注精确到事件粒度，有权威来源  
   缺点：成本极高（需要真实红队），无法自动化，标注滞后（事后分析）  
   数据集：DARPA TC E3/E5，THEIA，CADETS，CLEARSCOPE，FiveDirections

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   路径 B：Provenance Graph 反向追踪——从 IOC 自动扩散标签

   这是当前学术论文（PROGRAPHER、ThreaTrace、UNICORN 等）的主流方法：

     已知：某个已确认恶意的节点 N（比如 /tmp/malware.sh 被执行）
          ↓
     在 provenance graph 上做反向 BFS/DFS：
          N 的所有祖先节点（谁写了这个文件？谁调用了它？）
          N 的所有后代节点（它读了哪些文件？连了哪些 IP？）
          ↓
     把这个子图标记为 malicious，其余为 benign

   关键问题：这个初始的已知恶意节点哪来的？
   •  威胁情报 IOC（文件 hash、IP、域名）
   •  已知漏洞利用的特征（CVE）
   •  或者就是 DARPA TC 的人工标注结果

   代表工作：
   •  SLEUTH（USENIX Security 17）：从已知 IOC 反向溯源
   •  ATLAS（USENIX Security 21）：序列学习 + 自动扩散标签
   •  NODLINK（NDSS 24）：在线细粒度 APT 检测

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   路径 C：Benign Activity Extraction——先排除正常，剩下就是异常

   2025 年的新方向（arxiv 2503.19370）：

     不直接标恶意，而是：
     1. 用 NLP 对日志做聚类，提取"高频重复"模式 → benign
     2. 把这些 benign 模式从 provenance graph 里去掉
     3. 剩下的低频、异常的子图 → 可疑，供分析师审查

     在 DARPA TC 数据集上：
       - 6.8%~39% 的事件可被自动识别为 benign 并移除
       - dependency graph 规模缩减最多 52%

   适合 sysbox 的地方：我们的场景里，benign 事件很清晰——tracee 初始化噪声、容器 OS 的常规 systemd 调用、nginx 的 keepalive poll 等。

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   三、跨主机归因的具体技术方案

   方案 1：Tetragon（Cilium）— 工业界最成熟

   Tetragon 是 Cilium 的 eBPF 安全观测组件，它的跨主机能力来自：

     每个事件携带：
       - pod_name / namespace（Kubernetes 身份）
       - process.pid + process.binary
       - network: src_ip, dst_ip, src_port, dst_port
       - parent_exec_id（全局唯一，跨 fork/exec 链）

     跨主机关联：
       attacker pod exec_id=A123 → connect(dst=victim:80)
       victim pod accept(src=attacker_ip:54321) → 同一 TCP 四元组

   Tetragon 把 Kubernetes identity（pod label）注入到每个网络事件里，所以不需要 PID 跨节点传递，靠网络四元组 + 身份标签完成关联。

   对 sysbox 的启示：我们已经有 node_id，也有 connect 的 addr.sin_addr，缺的是 accept 事件里的 src_ip。加上这个就能做跨节点的 TCP 四元组关联。

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   方案 2：W3C PROV + 分布式 Trace Context

   分布式系统里的标准做法（OpenTelemetry 的 trace_id）：在请求本身里注入 trace context。

     HTTP 请求：
       GET / HTTP/1.1
       traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01

     这个 trace_id 在所有节点的日志里都出现 → 天然关联

   但在攻击场景里，攻击者当然不会主动注入 trace_id。所以这个方案只能用于受控 lab 里（我们的 agent 可以在工具调用时注入一个 session header），不适用于真实攻击检测。

   对 sysbox 的启示：这正是我们的 hook 可以做的事——在 agent 发出 HTTP 请求时注入 X-Sysbox-Step: ep-xxx:step-5，然后在受害机上的 nginx 日志里可以捕获这个 
   header，实现完美的跨节点归因。但这破坏了"agent 无感知"的设计原则。

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   方案 3：CamFlow — 内核级 Provenance，跨进程完整链

   CamFlow 在 Linux 内核里实现了 W3C PROV 标准的 provenance 捕获：

     每个 IPC、socket、文件操作都产生一条 provenance 边：
       process_A --write--> file_X
       process_B --read-->  file_X
       → A 的行为因果性地影响了 B

     跨主机：socket 操作产生 provenance 边，带 remote address
       process_A --sendmsg--> socket_S (dst=10.0.2.10:80)
       在远端：process_nginx --recvmsg--> socket_R (src=10.0.1.10:54321)
       用 TCP 四元组关联这两条边 → 跨主机 provenance 链

   CamFlow 生成的图可以直接喂给 UNICORN、ThreaTrace 等 GNN 模型。

   缺点：需要给每台机器打内核补丁，部署成本高。

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   四、对 sysbox 最实用的标注方案

   综合以上，适合 sysbox lab 场景的实施路径：

     层级 1（攻击者侧，已实现）
       PID 进程树归因
       → hook 记录 docker exec 的 host PID
       → tracee 事件的 pid/ppid 完整链路
       → 100% 精确，不需要时间窗口

     层级 2（网络跨节点，最高优先级）
       TCP 四元组关联
       → attacker: connect.addr.sin_addr + sin_port
       → victim:   accept.addr.sin_addr + sin_port（需要 tracee 捕获 accept）
       → 匹配 (src_ip, src_port, dst_ip, dst_port) → 同一连接

     层级 3（受害者本地因果，可选）
       从 accept 事件出发，在受害机上做进程树追踪
       → nginx worker 的 fork → execve（如果执行了命令）
       → 这就是 lateral movement 的 ground truth

     层级 4（Benign Baseline 去噪，后处理）
       跑 N 次 benign episode（no attack），记录正常行为模式
       → 从攻击 episode 的 events 里减去这些 benign 基线
       → 剩下的是净增量，标注质量大幅提升

   ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

   五、可参考的数据集和工具

   资源                    │ 用途                                              
   ------------------------│---------------------------------------------------
   DARPA TC E3/E5          │ 标注方法参考，包含 ground truth report 格式
   CamFlow                 │ 内核级 provenance 捕获，可替换 tracee 做更精确标注
   Tetragon                │ 工业级跨节点归因，可直接集成
   PROGRAPHER / ThreaTrace │ 从 provenance graph 自动检测的参考实现
   Atomic Red Team         │ 攻击剧本库，每个 TTP 有确定的预期 syscall

   Atomic Red Team 特别值得关注——它的设计思路和 sysbox 的 extraction rules 几乎一样：每个 TTP 对应一个确定性的攻击脚本，执行结果是已知的。可以直接用它的 YAML 定义来生成 
   sysbox 的 prediction 期望事件，这样 ground truth 就是 deterministic 的。