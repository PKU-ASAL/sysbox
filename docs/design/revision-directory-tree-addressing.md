# Revision 内容寻址：从单 HCL blob 到 project 目录树

日期：2026-09-06
状态：待评审
前置：`revision-and-upsert-apply.md`、`revision-and-upsert-apply-plan.md`

本文修正 `revision-and-upsert-apply-plan.md` 的一个粒度错误：**revision 应当是「整个 project 目录树」的内容寻址，不是「单 HCL blob」。** 该 plan 目前把 revision 定义为单个 HCL 文件，apply 时物化单个 `field.sysbox.hcl`——这会漏掉 project 目录里的 `files/` 和 `modules/`，导致内容寻址失效。

## 一、为什么单 HCL blob 不够

一个 sysbox project 是**目录树**，不是单文件：

```
project/
├── field.sysbox.hcl      # root：网络 + sensitive variable + module 引用
├── modules/
│   ├── web/main.hcl       # module 子目录
│   └── db/main.hcl
└── files/
    └── playbook.tar.gz    # provisioner "file" 引用的装配文件
```

> 根文件固定为 `field.sysbox.hcl`（本地 CLI 默认，见 `cmd/sysbox/commands/root.go`）；`main.hcl` 只是 module 子目录里的 fallback 文件名。

单 HCL blob 在三处漏：

| project 里有什么 | 单 blob 漏什么 |
|---|---|
| `provisioner "file" { source = "files/xxx" }` | files/ 里的装配文件 |
| `module "web" { source = "./modules/web" }` | 所有 module 子目录的 .hcl |
| 两者都有（复杂环境） | 整棵树的身份都不完整 |

证据：`provisioner "file"` 的 `source` 是相对 workspace 目录的路径（`resource_node.go` 的 `expandTilde`），`module` 的 `source` 是相对 root 所在目录的路径（`eval.go` 的 `resolveModuleSource`）。两者都依赖「workspace 是一个完整目录」，而 revision 若只物化一个 HCL 文件，这些相对路径就没有着落。

## 二、修正：revision 覆盖整个目录树

### 2.1 revision 的内容

revision 携带的是「目录树」：一个 `path → content` 的映射，而不是一个 HCL 字符串。

```go
// 伪码：revision 的内容模型
type Revision struct {
    Revision string            // digest，见 2.2
    Files    map[string][]byte // 相对 project 根的路径 → 文件内容
    Size     int               // 所有文件字节总数
    CreatedAt time.Time
}
```

`Files` 的 key 是相对 project 根的路径（`field.sysbox.hcl`、`modules/web/main.hcl`、`files/playbook.tar.gz`），value 是文件字节。

### 2.2 digest 算法：路径是 digest 的一部分

**digest 必须包含路径，不能只 hash 字节集合。** 只 hash 字节的灾难：

```
modules/web/main.hcl == modules/www/main.hcl   # 字节相同
```

若只 hash 字节，两个「不同 module 布局」算出同一个 digest，被当成同一个 revision——内容寻址的意义（内容不同 → digest 不同）就破了。更关键地，目录名通过 `source` 路径嵌进了 root 的内容：`module "web" { source = "./modules/web" }` 里的 `web` 是语义的一部分，改名必然牵动 root 的字节。所以「改目录名但内容没变」在 module 场景下不存在。

内容摘要算法（消费者应遵循同一字节级契约）：

1. 取 `Files` 的所有路径，字典序排序；
2. 对每个 `(path, content)`，写入 path 的 8 字节大端长度前缀 + path 字节 + content 的 8 字节大端长度前缀 + content 字节；
3. 全部拼接后 SHA256，输出 `sha256:<hex>`。

长度前缀防止拼接边界碰撞（`{"ab":"c"}` 与 `{"a":"bc"}` 在朴素拼接下会撞）。

### 2.3 `/v1/revisions` 接口：上传目录

`POST /v1/revisions` 从「上传单 HCL blob」改为「上传一个 project 目录」。可选形态：

- **multipart 上传**：一个字段携带目录的 tar/zip，或逐文件上传；
- **或 JSON**：`{"files": {"main.hcl": "...", "modules/web/main.hcl": "...", ...}}`。

响应不变：`201 {"revision": "sha256:<hex>"}`。幂等语义不变：同一目录内容重复 publish 返回同一 digest。

（具体选 multipart 还是 JSON，由实现者定；约束是「能表达目录树的 path→content 映射」。）

### 2.4 apply 物化整个目录

`StartApply` 从 revision 取目录树，**物化整个目录到 workspace**，而不是写单个 `field.sysbox.hcl`：

```
revision.Files  →  逐个写到 workspace/<topology>/<相对路径>
```

物化后，`provisioner "file"` 的 `source = "files/xxx"` 和 `module` 的 `source = "./modules/web"` 相对路径自然成立。`UpsertHCL` 相应改为 `UpsertProject`（幂等写整个目录树：同内容 no-op，见 `revision-and-upsert-apply-plan.md` Task 2 Step 6 的幂等语义）。

## 三、不调整的：module 机制

**保持「root 单文件 + module 目录树」，不改成 Terraform 的「平铺多文件合并」。** 理由：

- 实验拓扑的本质是「组件组合」（web 节点 + db 节点 + 攻击机），module 的「显式边界 + 输入输出 + 复用」匹配组件；Terraform 平铺的「拆 .tf」是编辑便利，不是结构。
- 本设计以根文件和显式模块为解析边界，平铺多文件不属于当前契约。
- 改平铺需要调整 parser/eval，应依据通用拓扑的组合需求单独设计，不由单一消费者决定。

`resolveModuleSource` 已经是「module = 目录，找目录里 `*.sysbox.hcl` / `main.hcl`」，revision 覆盖目录树后无需改动 module 机制本身，只需保证 module 子目录被包含在 revision 的 `Files` 里。

## 四、对 `revision-and-upsert-apply-plan.md` 的具体改动

| plan 里的原样 | 改为 |
|---|---|
| Task 1：`GlobalRevision{HCL string}` | `Revision{Files map[string][]byte}`，digest 见 2.2 |
| Task 1 Step 6：`handlePublishRevision` 读 `io.ReadAll(r.Body)` 单 blob | 读目录树（multipart 或 JSON），算目录树 digest |
| Task 2 Step 5：`StartApply` 取 `rev.HCL` 写单文件 | 取 `rev.Files` 物化整个目录 |
| Task 2 Step 6：`WorkspaceService.UpsertHCL` | `UpsertProject`（幂等写目录树） |
| 测试 `TestPublishRevisionIsContentAddressed` | 补：目录树含 files/ 和 modules/ 时 digest 正确；改目录名（连带 source）→ digest 变；不同布局同字节 → digest 不同 |

## 五、不改动的既有机制（确认已就绪）

- **canary / sensitive**：`variable "flag" { sensitive = true }` + `secret://input/flag` 引用，明文只在执行期物化（`VariableBindings` + `InputResolver`）。provisioner exec 的 `Program`/`Args`/`Environment` 均做 secret 解析，复杂装配（Ansible）经 `Environment` 传明文，不进命令行参数。
- **conditions / check-assert**：`status.conditions[]` + 三态 `ConditionStatus` 已实现。
- **upsert apply**：`POST /v1/topologies/{name}/apply` 的 upsert 语义、`deadline_at`、删旧端点，与本文正交，不变。

## 六、验收

- [ ] `POST /v1/revisions` 接受一个含 `field.sysbox.hcl` + `modules/` + `files/` 的目录，返回稳定 digest；
- [ ] 同一目录重复 publish → 同一 digest；改目录名（连带 source）→ 不同 digest；
- [ ] `modules/web/main.hcl` 与 `modules/www/main.hcl` 字节相同但路径不同 → 不同 digest；
- [ ] apply 物化后，`provisioner "file" { source = "files/xxx" }` 与 `module { source = "./modules/web" }` 能解析；
- [ ] 出题者本地 `sysbox up ./project`（目录）与 API 路径（publish → apply）行为一致。
