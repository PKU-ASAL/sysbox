# sysbox MVP Phase 1 — Hello World Field

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建 sysbox CLI v0.1：一个能用 HCL 描述多容器 Linux 拓扑、带 linux-bridge 网络、能 `plan/apply/destroy` 的工具。能跑 `sysbox apply hello-world.hcl` 起 2 个 Alpine 容器互 ping 即完成。

**Architecture:** Go CLI，HCL v2 parser → resource DAG → 和 state file 对 diff 算 plan → 拓扑序 walk graph 调用 providers 执行。Phase 1 providers 是 **in-process Go package**（不走 go-plugin，降低起步复杂度；Phase 3 加 Firecracker 和 libvirt 时再改成 go-plugin gRPC）。

**Tech Stack:** Go 1.22+, `github.com/hashicorp/hcl/v2`, `github.com/zclconf/go-cty`, `github.com/docker/docker/client`, `github.com/vishvananda/netlink`, `github.com/vishvananda/netns`, `github.com/google/nftables`, `github.com/stretchr/testify`

**Out of scope (留给 Phase 2+):** sensors, session cgroups, Prediction Schema, Matcher, Firecracker, libvirt, replay bundle.

---

## 仓库初始态

Phase 1 从空目录开始。启动时在 `sysbox/` 下执行所有命令（与 `docs/` 同级）。Git 仓库在 Task 0 初始化。

---

## 文件结构（Phase 1 结束时）

```
sysbox/
├── go.mod
├── go.sum
├── Makefile
├── README.md
│
├── cmd/
│   └── sysbox/
│       ├── main.go                          # CLI 入口，cobra 路由
│       └── commands/
│           ├── root.go                      # root command + global flags
│           ├── init_cmd.go                  # sysbox init
│           ├── plan_cmd.go                  # sysbox plan
│           ├── apply_cmd.go                 # sysbox apply
│           ├── destroy_cmd.go               # sysbox destroy
│           ├── state_cmd.go                 # sysbox state list
│           ├── show_cmd.go                  # sysbox show
│           └── output_cmd.go                # sysbox output
│
├── pkg/
│   ├── config/
│   │   ├── schema.go                        # 资源类型 Go 结构体 (SubstrateConfig, Node, Network, ...)
│   │   ├── parser.go                        # HCL v2 解析器 (ParseFile, ParseDir)
│   │   └── parser_test.go
│   │
│   ├── graph/
│   │   ├── graph.go                         # Resource DAG (节点、边、ID)
│   │   ├── walker.go                        # TopoWalk (forward), ReverseWalk (destroy 用)
│   │   └── graph_test.go
│   │
│   ├── state/
│   │   ├── state.go                         # State 结构体 + JSON marshal
│   │   ├── manager.go                       # Load/Save with file lock
│   │   └── state_test.go
│   │
│   ├── runtime/
│   │   ├── plan.go                          # Plan 计算 (graph × state → add/change/destroy)
│   │   ├── apply.go                         # 执行 plan (forward walk)
│   │   ├── destroy.go                       # 执行反向 destroy
│   │   └── runtime_test.go
│   │
│   ├── substrate/
│   │   ├── substrate.go                     # Substrate interface (核心抽象)
│   │   ├── types.go                         # NodeSpec / NodeHandle / NIC / ImageSpec / Capabilities
│   │   └── registry.go                      # 已注册 substrate 列表
│   │
│   └── provider/
│       ├── docker/
│       │   ├── docker.go                    # DockerSubstrate 实现 Substrate 接口
│       │   ├── image.go                     # PrepareImage 逻辑
│       │   ├── node.go                      # CreateNode / StartNode / DestroyNode
│       │   ├── exec.go                      # ExecInNode / CopyToNode / CopyFromNode
│       │   ├── nic.go                       # AttachNIC (把 veth 挂进容器 netns)
│       │   └── docker_test.go
│       │
│       └── network/
│           ├── provider.go                  # NetworkProvider (非 substrate 接口)
│           ├── bridge.go                    # Bridge 创建/删除
│           ├── veth.go                      # veth pair 创建
│           ├── netns.go                     # netns 操作封装
│           ├── firewall.go                  # nftables 规则渲染 (Phase 1 简化版, 仅 drop/accept)
│           └── network_test.go
│
├── examples/
│   ├── hello-world/
│   │   └── field.sysbox.hcl                 # 2 alpine 容器 + bridge
│   └── two-networks/
│       └── field.sysbox.hcl                 # 双 bridge + firewall 示例
│
└── tests/
    ├── e2e/
    │   ├── helloworld_test.go               # 启一个 field, 两个容器互 ping, 关掉
    │   └── e2e_common.go                    # 共享 helpers
    └── testdata/
        ├── valid_field.hcl
        └── invalid_field.hcl
```

**文件职责总览：**

| 层 | 文件 | 职责 |
|---|---|---|
| CLI | `cmd/sysbox/**` | 命令行入口和用户交互 |
| 配置 | `pkg/config/**` | HCL 解析、资源类型定义 |
| 图 | `pkg/graph/**` | DAG 构建、拓扑序遍历 |
| 状态 | `pkg/state/**` | State file 读写、并发锁 |
| 运行时 | `pkg/runtime/**` | Plan 计算和 Apply/Destroy 执行 |
| 抽象 | `pkg/substrate/**` | Substrate 接口（跨 provider 的通用约定） |
| Provider | `pkg/provider/docker/**` | Docker 容器作为一种 substrate |
| Provider | `pkg/provider/network/**` | 网络原语（不是 substrate，是基础设施） |
| E2E | `tests/e2e/**` | 端到端集成测试 |

---

## Task 0: 项目脚手架

**Files:**
- Create: `sysbox/go.mod`
- Create: `sysbox/Makefile`
- Create: `sysbox/.gitignore`
- Create: `sysbox/README.md`

- [ ] **Step 1: 进入 sysbox 目录并初始化 git**

```bash
cd /home/jiandong/workspace/oslab/sysarmor/sysfield-project/sysbox
git init
```

- [ ] **Step 2: 创建 go.mod**

```bash
go mod init github.com/oslab/sysbox
```

Expected: `go.mod` file with `module github.com/oslab/sysbox` and `go 1.22`.

- [ ] **Step 3: 添加核心依赖**

```bash
go get github.com/hashicorp/hcl/v2@latest
go get github.com/zclconf/go-cty@latest
go get github.com/docker/docker/client@latest
go get github.com/vishvananda/netlink@latest
go get github.com/vishvananda/netns@latest
go get github.com/google/nftables@latest
go get github.com/spf13/cobra@latest
go get github.com/stretchr/testify@latest
```

- [ ] **Step 4: 创建 .gitignore**

```
# binaries
/bin/
/sysbox
*.test

# state / runs
/runs/
*.tfstate
*.tfstate.backup

# IDE
.vscode/
.idea/

# macOS
.DS_Store
```

- [ ] **Step 5: 创建 Makefile**

```makefile
.PHONY: build test lint clean e2e

build:
	go build -o bin/sysbox ./cmd/sysbox

test:
	go test ./pkg/... -race -cover

lint:
	go vet ./...
	gofmt -l . | diff -u /dev/null -

clean:
	rm -rf bin/ runs/

e2e:
	go test ./tests/e2e/... -tags=e2e -v -timeout 5m

install: build
	install -m 0755 bin/sysbox /usr/local/bin/sysbox
```

- [ ] **Step 6: 创建最小 README.md**

```markdown
# sysbox

AI 红队的 Terraform —— 一键搭起 Linux 攻防战场。

**Status:** MVP Phase 1 — Hello World field

See [docs/specs/2026-05-07-sysbox-design.md](docs/specs/2026-05-07-sysbox-design.md) for the full design.

## Quickstart

```bash
make build
./bin/sysbox apply examples/hello-world/field.sysbox.hcl
./bin/sysbox state list
./bin/sysbox destroy
```
```

- [ ] **Step 7: 创建目录骨架（空目录）**

```bash
mkdir -p cmd/sysbox/commands pkg/{config,graph,state,runtime,substrate}/. pkg/provider/{docker,network}/. examples/hello-world examples/two-networks tests/e2e tests/testdata
```

- [ ] **Step 8: 验证构建空项目**

创建 `cmd/sysbox/main.go` 占位：

```go
package main

func main() {}
```

然后构建：

```bash
go build -o bin/sysbox ./cmd/sysbox
```

Expected: `bin/sysbox` created successfully.

- [ ] **Step 9: 首次提交**

```bash
git add go.mod go.sum Makefile .gitignore README.md cmd/sysbox/main.go
git commit -m "chore: initial sysbox project scaffolding"
```

---

## Task 1: State 文件数据结构

**Files:**
- Create: `sysbox/pkg/state/state.go`
- Create: `sysbox/pkg/state/state_test.go`

- [ ] **Step 1: 写失败测试 `state_test.go`**

```go
package state

import (
	"testing"
	"github.com/stretchr/testify/require"
)

func TestStateRoundTrip(t *testing.T) {
	original := &State{
		Version: 1,
		RunID:   "test-run-01",
		Resources: []Resource{
			{
				Type:     "sysbox_node",
				Name:     "web",
				Provider: "docker",
				Instance: map[string]any{
					"id":         "container-abc123",
					"image":      "alpine:3.19",
					"running":    true,
				},
			},
		},
	}

	bytes, err := original.Marshal()
	require.NoError(t, err)

	decoded, err := Unmarshal(bytes)
	require.NoError(t, err)
	require.Equal(t, original.RunID, decoded.RunID)
	require.Len(t, decoded.Resources, 1)
	require.Equal(t, "web", decoded.Resources[0].Name)
}

func TestStateFindResource(t *testing.T) {
	s := &State{
		Resources: []Resource{
			{Type: "sysbox_node", Name: "web"},
			{Type: "sysbox_node", Name: "db"},
		},
	}

	r := s.FindResource("sysbox_node", "web")
	require.NotNil(t, r)
	require.Equal(t, "web", r.Name)

	require.Nil(t, s.FindResource("sysbox_node", "notfound"))
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./pkg/state/... -run TestState -v
```

Expected: FAIL with undefined `State`, `Resource`, `Marshal`, `Unmarshal`.

- [ ] **Step 3: 实现 `state.go`**

```go
// Package state manages sysbox's persistent state file.
//
// Each sysbox apply writes resource entries to a JSON state file.
// The state is the single source of truth for what's currently deployed.
package state

import (
	"encoding/json"
	"fmt"
)

type State struct {
	Version   int        `json:"version"`
	RunID     string     `json:"run_id"`
	Resources []Resource `json:"resources"`
}

type Resource struct {
	Type     string         `json:"type"`     // e.g. "sysbox_node"
	Name     string         `json:"name"`     // e.g. "web"
	Provider string         `json:"provider"` // e.g. "docker"
	Instance map[string]any `json:"instance"` // provider-opaque fields
}

func (s *State) Marshal() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

func Unmarshal(data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &s, nil
}

func (s *State) FindResource(typ, name string) *Resource {
	for i := range s.Resources {
		if s.Resources[i].Type == typ && s.Resources[i].Name == name {
			return &s.Resources[i]
		}
	}
	return nil
}

func (s *State) AddResource(r Resource) {
	s.Resources = append(s.Resources, r)
}

func (s *State) RemoveResource(typ, name string) {
	out := s.Resources[:0]
	for _, r := range s.Resources {
		if r.Type == typ && r.Name == name {
			continue
		}
		out = append(out, r)
	}
	s.Resources = out
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./pkg/state/... -v
```

Expected: PASS.

- [ ] **Step 5: 提交**

```bash
git add pkg/state/
git commit -m "feat(state): add State and Resource types with JSON round-trip"
```

---

## Task 2: State 文件管理（Load/Save with file lock）

**Files:**
- Create: `sysbox/pkg/state/manager.go`
- Modify: `sysbox/pkg/state/state_test.go`

- [ ] **Step 1: 扩展 `state_test.go` 加 Manager 测试**

```go
func TestManagerSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	mgr := NewManager(path)

	s := &State{Version: 1, RunID: "r1", Resources: []Resource{
		{Type: "sysbox_node", Name: "web", Provider: "docker", Instance: map[string]any{"id": "abc"}},
	}}

	require.NoError(t, mgr.Save(s))

	loaded, err := mgr.Load()
	require.NoError(t, err)
	require.Equal(t, "r1", loaded.RunID)
	require.Len(t, loaded.Resources, 1)
}

func TestManagerLoadMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(filepath.Join(dir, "state.json"))

	s, err := mgr.Load()
	require.NoError(t, err)
	require.Equal(t, 0, len(s.Resources))
}
```

Remember to add `"path/filepath"` to the import list.

- [ ] **Step 2: 运行确认失败**

```bash
go test ./pkg/state/... -run TestManager -v
```

Expected: FAIL with undefined `NewManager`.

- [ ] **Step 3: 实现 `manager.go`**

```go
package state

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type Manager struct {
	path string
}

func NewManager(path string) *Manager {
	return &Manager{path: path}
}

// Load reads the state file. Missing file returns empty state, not error.
func (m *Manager) Load() (*State, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &State{Version: 1}, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	return Unmarshal(data)
}

// Save atomically writes state to disk: write temp file, then rename.
// Acquires a file lock to prevent concurrent writers.
func (m *Manager) Save(s *State) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	lock := flock.New(m.path + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("acquire state lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("state is locked by another process")
	}
	defer lock.Unlock()

	data, err := s.Marshal()
	if err != nil {
		return err
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	return os.Rename(tmp, m.path)
}
```

Remember to add `"errors"` to state.go import; and run `go get github.com/gofrs/flock`.

- [ ] **Step 4: 运行 + 确认通过**

```bash
go test ./pkg/state/... -v
```

Expected: all PASS.

- [ ] **Step 5: 提交**

```bash
git add pkg/state/manager.go pkg/state/state_test.go go.mod go.sum
git commit -m "feat(state): add Manager with atomic save and file lock"
```

---

## Task 3: HCL Schema 的 Go 结构体

**Files:**
- Create: `sysbox/pkg/config/schema.go`

- [ ] **Step 1: 写 schema.go（Phase 1 资源类型子集）**

```go
// Package config defines sysbox's HCL schema and parser.
//
// Phase 1 supports:
//   - substrate block (only type="docker")
//   - sysbox_image, sysbox_node, sysbox_network, sysbox_link,
//     sysbox_firewall, sysbox_router, sysbox_ssh_access
//
// Firecracker/libvirt substrates and sensor resources are Phase 2/3.
package config

// Root is the top-level parsed HCL document.
type Root struct {
	Substrates []SubstrateBlock `hcl:"substrate,block"`
	Resources  []ResourceBlock  `hcl:"resource,block"`
}

// SubstrateBlock corresponds to:
//   substrate "docker" { alias = "light" }
type SubstrateBlock struct {
	Type   string   `hcl:"type,label"` // "docker" | "firecracker" | "libvirt"
	Alias  string   `hcl:"alias"`
	Remain HCLBody  `hcl:",remain"`    // substrate-specific fields
}

// ResourceBlock is every "resource" in the HCL file:
//   resource "sysbox_node" "web" { image = ...; links = [...] }
type ResourceBlock struct {
	Type   string  `hcl:"type,label"` // "sysbox_node" | "sysbox_network" | ...
	Name   string  `hcl:"name,label"`
	Remain HCLBody `hcl:",remain"`    // resource-specific fields
}

// Typed representations after second-pass decoding.

type NodeConfig struct {
	Name      string       `hcl:"name,optional"`
	Image     string       `hcl:"image"`      // reference to sysbox_image
	Substrate string       `hcl:"substrate"`  // reference to substrate block
	Vcpus     int          `hcl:"vcpus,optional"`
	Memory    string       `hcl:"memory,optional"`
	Env       map[string]string `hcl:"env,optional"`
	Links     []LinkConfig `hcl:"links,optional"`
}

type LinkConfig struct {
	Network string `hcl:"network"`        // reference to sysbox_network
	IP      string `hcl:"ip"`             // e.g. "10.0.1.10/24"
	Gateway string `hcl:"gw,optional"`
}

type NetworkConfig struct {
	CIDR string `hcl:"cidr"`
	Type string `hcl:"type,optional"`     // default "bridge"
}

type ImageConfig struct {
	Substrate string `hcl:"substrate"`
	DockerRef string `hcl:"docker_ref,optional"`
	Rootfs    string `hcl:"rootfs,optional"` // for Firecracker/libvirt, Phase 1 ignored
	Size      string `hcl:"size,optional"`
}

type FirewallConfig struct {
	AttachTo string            `hcl:"attach_to"`
	Rules    []FirewallRule    `hcl:"rules"`
}

type FirewallRule struct {
	Proto   string `hcl:"proto"`    // "tcp" | "udp" | "all"
	DPort   int    `hcl:"dport,optional"`
	SrcNet  string `hcl:"src_net,optional"`
	Action  string `hcl:"action"`   // "accept" | "drop"
}

type RouterConfig struct {
	Substrate  string                       `hcl:"substrate"`
	Image      string                       `hcl:"image"`
	Interfaces map[string]RouterInterface   `hcl:"interfaces"`
	NatFrom    string                       `hcl:"nat_from,optional"`
	NatTo      string                       `hcl:"nat_to,optional"`
}

type RouterInterface struct {
	Network string `hcl:"network"`
	IP      string `hcl:"ip"`
}

type SSHAccessConfig struct {
	Node           string   `hcl:"node"`
	AuthorizedKeys []string `hcl:"authorized_keys"`
	BindIP         string   `hcl:"bind_ip,optional"`
	Port           int      `hcl:"port,optional"`
}

// HCLBody is a type alias for remain clauses.
type HCLBody interface{}
```

- [ ] **Step 2: 确认编译（这步不运行测试）**

```bash
go build ./pkg/config/...
```

Expected: success.

- [ ] **Step 3: 提交**

```bash
git add pkg/config/schema.go
git commit -m "feat(config): add HCL schema types for Phase 1 resources"
```

---

## Task 4: HCL Parser

**Files:**
- Create: `sysbox/pkg/config/parser.go`
- Create: `sysbox/pkg/config/parser_test.go`
- Create: `sysbox/tests/testdata/valid_field.hcl`

- [ ] **Step 1: 创建测试数据 `valid_field.hcl`**

```hcl
substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "dmz" {
  cidr = "10.0.1.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = substrate.docker.light
  docker_ref = "alpine:3.19"
}

resource "sysbox_node" "web" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light
  links = [
    { network = sysbox_network.dmz.id, ip = "10.0.1.10/24" },
  ]
}

resource "sysbox_node" "client" {
  image     = sysbox_image.alpine.id
  substrate = substrate.docker.light
  links = [
    { network = sysbox_network.dmz.id, ip = "10.0.1.20/24" },
  ]
}
```

- [ ] **Step 2: 写失败测试 `parser_test.go`**

```go
package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFile(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "valid_field.hcl")

	root, err := ParseFile(path)
	require.NoError(t, err)

	require.Len(t, root.Substrates, 1)
	require.Equal(t, "docker", root.Substrates[0].Type)
	require.Equal(t, "light", root.Substrates[0].Alias)

	require.Len(t, root.Resources, 4)

	// Find the network
	network := findResource(root, "sysbox_network", "dmz")
	require.NotNil(t, network)

	// Find the web node
	web := findResource(root, "sysbox_node", "web")
	require.NotNil(t, web)
}

func findResource(root *Root, typ, name string) *ResourceBlock {
	for i := range root.Resources {
		if root.Resources[i].Type == typ && root.Resources[i].Name == name {
			return &root.Resources[i]
		}
	}
	return nil
}
```

- [ ] **Step 3: 运行确认失败**

```bash
go test ./pkg/config/... -v
```

Expected: FAIL with undefined `ParseFile`.

- [ ] **Step 4: 实现 `parser.go`**

```go
package config

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/gohcl"
)

// ParseFile parses an HCL file into the Root structure.
//
// Note: this does a shallow first-pass decode of substrate/resource blocks.
// Inner fields (NodeConfig, NetworkConfig, etc.) are decoded on demand
// using DecodeResource() because different resource types have different
// schemas.
func ParseFile(path string) (*Root, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read HCL file: %w", err)
	}

	parser := hclparse.NewParser()
	file, diag := parser.ParseHCL(data, path)
	if diag.HasErrors() {
		return nil, fmt.Errorf("parse HCL: %s", diag.Error())
	}

	var root Root
	if diag := gohcl.DecodeBody(file.Body, nil, &root); diag.HasErrors() {
		return nil, fmt.Errorf("decode HCL: %s", diag.Error())
	}
	return &root, nil
}

// DecodeResource decodes a resource block's inner fields into the given
// target struct (e.g. *NodeConfig, *NetworkConfig). Caller picks the target
// based on r.Type.
func DecodeResource(r *ResourceBlock, target any) error {
	body, ok := r.Remain.(hcl.Body)
	if !ok {
		return fmt.Errorf("resource %s.%s: body not an hcl.Body", r.Type, r.Name)
	}
	// Phase 1 passes nil eval context; later phases will resolve references
	// (substrate.docker.light, sysbox_image.alpine.id) here.
	if diag := gohcl.DecodeBody(body, nil, target); diag.HasErrors() {
		return fmt.Errorf("decode resource %s.%s: %s", r.Type, r.Name, diag.Error())
	}
	return nil
}
```

- [ ] **Step 5: 运行测试确认通过（基础解析）**

```bash
go test ./pkg/config/... -run TestParseFile -v
```

Expected: PASS.

**说明**：完整的 HCL 引用解析（`sysbox_image.alpine.id`）是 Phase 2 加进来的 EvalContext 工作；Phase 1 的测试数据构造得足够简单，只用字符串常量即可。下个任务 Graph 层会消费 ResourceBlock 列表建立依赖，此时不需要解析引用本身。

- [ ] **Step 6: 加一个 invalid HCL 的 happy-path 测试**

在 `tests/testdata/` 下创建 `invalid_field.hcl`：

```hcl
substrate "docker" {
  alias = "missing_closing_brace"
```

测试：

```go
func TestParseFileInvalid(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "invalid_field.hcl")
	_, err := ParseFile(path)
	require.Error(t, err)
}
```

运行：

```bash
go test ./pkg/config/... -v
```

Expected: all PASS including new failure test.

- [ ] **Step 7: 提交**

```bash
git add pkg/config/parser.go pkg/config/parser_test.go tests/testdata/
git commit -m "feat(config): add HCL parser for substrate and resource blocks"
```

---

## Task 5: Resource Graph (DAG)

**Files:**
- Create: `sysbox/pkg/graph/graph.go`
- Create: `sysbox/pkg/graph/walker.go`
- Create: `sysbox/pkg/graph/graph_test.go`

- [ ] **Step 1: 写 DAG 测试 `graph_test.go`**

```go
package graph

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAndTopoWalk(t *testing.T) {
	g := New()

	g.AddNode("network", "dmz", nil)
	g.AddNode("image", "alpine", nil)
	g.AddNode("node", "web", []Ref{
		{Type: "network", Name: "dmz"},
		{Type: "image", Name: "alpine"},
	})

	order, err := g.TopoSort()
	require.NoError(t, err)

	// web depends on network+image → must come after them
	webIdx := indexOf(order, "node", "web")
	netIdx := indexOf(order, "network", "dmz")
	imgIdx := indexOf(order, "image", "alpine")
	require.Greater(t, webIdx, netIdx)
	require.Greater(t, webIdx, imgIdx)
}

func TestCycleDetection(t *testing.T) {
	g := New()
	g.AddNode("a", "1", []Ref{{Type: "b", Name: "1"}})
	g.AddNode("b", "1", []Ref{{Type: "a", Name: "1"}})

	_, err := g.TopoSort()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle")
}

func TestReverseWalk(t *testing.T) {
	g := New()
	g.AddNode("network", "dmz", nil)
	g.AddNode("node", "web", []Ref{{Type: "network", Name: "dmz"}})

	order, err := g.ReverseTopoSort()
	require.NoError(t, err)

	// destroy order: node before network
	webIdx := indexOf(order, "node", "web")
	netIdx := indexOf(order, "network", "dmz")
	require.Less(t, webIdx, netIdx)
}

func indexOf(order []NodeID, typ, name string) int {
	for i, id := range order {
		if id.Type == typ && id.Name == name {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./pkg/graph/... -v
```

Expected: FAIL with undefined types.

- [ ] **Step 3: 实现 `graph.go`**

```go
// Package graph builds a resource DAG from parsed HCL and walks it
// in topological order (for apply) or reverse topological order (for destroy).
package graph

import (
	"fmt"
)

type NodeID struct {
	Type string // e.g. "sysbox_node"
	Name string // e.g. "web"
}

func (id NodeID) String() string {
	return fmt.Sprintf("%s.%s", id.Type, id.Name)
}

type Ref = NodeID // A reference is the same shape as a NodeID.

type Node struct {
	ID   NodeID
	Deps []Ref
	Data any // resource-specific struct (NodeConfig, NetworkConfig, ...)
}

type Graph struct {
	nodes map[NodeID]*Node
}

func New() *Graph {
	return &Graph{nodes: make(map[NodeID]*Node)}
}

func (g *Graph) AddNode(typ, name string, deps []Ref) *Node {
	id := NodeID{Type: typ, Name: name}
	n := &Node{ID: id, Deps: deps}
	g.nodes[id] = n
	return n
}

func (g *Graph) SetData(typ, name string, data any) {
	if n, ok := g.nodes[NodeID{typ, name}]; ok {
		n.Data = data
	}
}

func (g *Graph) Get(typ, name string) *Node {
	return g.nodes[NodeID{typ, name}]
}

func (g *Graph) All() []*Node {
	out := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, n)
	}
	return out
}
```

- [ ] **Step 4: 实现 `walker.go`（Kahn 算法）**

```go
package graph

import "fmt"

// TopoSort returns node IDs in dependency order (deps before dependents).
// Returns an error if the graph contains a cycle.
func (g *Graph) TopoSort() ([]NodeID, error) {
	// Build reverse adjacency: for each edge dep→node, we need dep's neighbors.
	inDegree := make(map[NodeID]int)
	neighbors := make(map[NodeID][]NodeID)

	for id := range g.nodes {
		inDegree[id] = 0
	}

	for id, n := range g.nodes {
		for _, dep := range n.Deps {
			if _, ok := g.nodes[dep]; !ok {
				return nil, fmt.Errorf("resource %s references unknown %s", id, dep)
			}
			neighbors[dep] = append(neighbors[dep], id)
			inDegree[id]++
		}
	}

	// Start with all nodes that have no deps.
	var queue []NodeID
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var order []NodeID
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)

		for _, next := range neighbors[cur] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(order) != len(g.nodes) {
		return nil, fmt.Errorf("graph has cycle: resolved %d of %d nodes", len(order), len(g.nodes))
	}
	return order, nil
}

// ReverseTopoSort is TopoSort reversed (dependents before deps).
// Used by destroy: tear down dependents first.
func (g *Graph) ReverseTopoSort() ([]NodeID, error) {
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	reversed := make([]NodeID, len(order))
	for i, id := range order {
		reversed[len(order)-1-i] = id
	}
	return reversed, nil
}
```

- [ ] **Step 5: 运行测试确认通过**

```bash
go test ./pkg/graph/... -v
```

Expected: all PASS.

- [ ] **Step 6: 提交**

```bash
git add pkg/graph/
git commit -m "feat(graph): add resource DAG with topo sort and cycle detection"
```

---

## Task 6: Substrate 接口与共享类型

**Files:**
- Create: `sysbox/pkg/substrate/substrate.go`
- Create: `sysbox/pkg/substrate/types.go`
- Create: `sysbox/pkg/substrate/registry.go`

- [ ] **Step 1: 写接口和 types（无测试，纯定义）**

`pkg/substrate/types.go`:

```go
// Package substrate defines the abstraction that every substrate driver
// (docker, firecracker, libvirt) must implement.
//
// Phase 1 only has docker. Phase 3 adds firecracker and libvirt.
package substrate

import (
	"context"
	"io"
)

type ImageSpec struct {
	DockerRef string // for docker substrate
	Rootfs    string // for fc/libvirt (Phase 3)
	Size      string // e.g. "4GiB" (Phase 3)
}

type ImageRef struct {
	ID         string // substrate-opaque identifier
	Repository string // human-readable (e.g. "alpine:3.19")
}

type NodeSpec struct {
	Name   string
	Image  ImageRef
	VCPUs  int               // Phase 3
	Memory string            // Phase 3
	Env    map[string]string
}

type NodeHandle struct {
	ID         string         // substrate-opaque ID (docker container ID, etc.)
	Attributes map[string]any // free-form metadata
}

type ExecSpec struct {
	Cmd     []string
	Env     map[string]string
	WorkDir string
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type NIC struct {
	Kind     string // "veth" | "tap"
	HostEnd  string // e.g. "veth-abc123"
	GuestEnd string // only for veth
	MAC      string
	IP       string // CIDR notation e.g. "10.0.1.10/24"
	Gateway  string
	MTU      int
}

type Capabilities struct {
	SharedKernel    bool   // docker=true, fc=false
	SupportsWindows bool   // libvirt=true
	BootTime        string // "ms" | "seconds"
	NICType         string // "veth" | "tap"
}

// ObservationTarget tells the sensor provider how to attach to this node.
// Phase 1 uses only "host-pid-namespace" (docker); virtio-serial comes in Phase 3.
type ObservationTarget struct {
	Kind  string // "host-pid-namespace" | "virtio-serial"
	Value string // PID or socket path
}
```

- [ ] **Step 2: 定义接口 `substrate.go`**

```go
package substrate

import (
	"context"
	"io"
)

// Substrate is the contract every node provider must fulfill.
// A "substrate" (docker, firecracker, libvirt) creates and manages nodes,
// where a node is a unit of isolation running a guest (container, microVM, VM).
type Substrate interface {
	// Name is a stable identifier like "docker", "firecracker", "libvirt".
	Name() string

	// Capabilities describes what this substrate can do.
	Capabilities() Capabilities

	// PrepareImage ensures the image is available (pull, build, whatever).
	// Returns a reference that later CreateNode calls use.
	PrepareImage(ctx context.Context, spec ImageSpec) (ImageRef, error)

	// CreateNode allocates the node (does NOT start it yet). Network
	// attachment happens in a later AttachNIC step so that the network
	// provider can prepare veth/tap devices first.
	CreateNode(ctx context.Context, spec NodeSpec) (NodeHandle, error)

	// StartNode transitions the node from created to running.
	StartNode(ctx context.Context, handle NodeHandle) error

	// StopNode gracefully stops a running node.
	StopNode(ctx context.Context, handle NodeHandle) error

	// DestroyNode removes the node and any state.
	DestroyNode(ctx context.Context, handle NodeHandle) error

	// ExecInNode runs a command inside the node.
	ExecInNode(ctx context.Context, handle NodeHandle, spec ExecSpec) (ExecResult, error)

	// CopyToNode copies a file from host to node.
	CopyToNode(ctx context.Context, handle NodeHandle, src, dst string) error

	// CopyFromNode copies a file from node to host.
	CopyFromNode(ctx context.Context, handle NodeHandle, src, dst string) error

	// AttachTTY returns an interactive TTY to the node (Phase 2+).
	AttachTTY(ctx context.Context, handle NodeHandle) (io.ReadWriteCloser, error)

	// AttachNIC plugs a pre-created network interface into the node.
	// Network provider has already created the veth/tap; this call wires it in.
	AttachNIC(ctx context.Context, handle NodeHandle, nic NIC) error

	// ObservationHook tells the sensor provider how to observe this node.
	// Phase 1: docker returns host-pid-namespace. Phase 3: fc/libvirt return virtio-serial.
	ObservationHook(ctx context.Context, handle NodeHandle) (ObservationTarget, error)
}
```

- [ ] **Step 3: 实现 `registry.go`**

```go
package substrate

import (
	"fmt"
	"sync"
)

var (
	registry   = make(map[string]Substrate)
	registryMu sync.Mutex
)

// Register adds a substrate under its name. Call from provider packages'
// init() functions (via an explicit registration call from main()).
func Register(s Substrate) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[s.Name()] = s
}

// Get returns a registered substrate by name. Returns an error if not found.
func Get(name string) (Substrate, error) {
	registryMu.Lock()
	defer registryMu.Unlock()
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("substrate %q not registered", name)
	}
	return s, nil
}

// List returns all registered substrate names (for diagnostics).
func List() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
```

- [ ] **Step 4: 确认编译**

```bash
go build ./pkg/substrate/...
```

Expected: success.

- [ ] **Step 5: 提交**

```bash
git add pkg/substrate/
git commit -m "feat(substrate): define Substrate interface and registry"
```

---

## Task 7: Docker Substrate — 镜像准备

**Files:**
- Create: `sysbox/pkg/provider/docker/docker.go`
- Create: `sysbox/pkg/provider/docker/image.go`
- Create: `sysbox/pkg/provider/docker/docker_test.go`

**前提**：测试机需要有 Docker daemon 可达（`docker ps` 能跑）。没有 Docker 的 CI 环境用 build tag `//go:build docker` 隔离。

- [ ] **Step 1: 写 docker.go 骨架**

```go
// Package docker implements the Substrate interface using the Docker daemon.
package docker

import (
	"context"

	"github.com/docker/docker/client"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Substrate is the Docker implementation of substrate.Substrate.
type Substrate struct {
	cli *client.Client
}

// New connects to the local Docker daemon.
func New() (*Substrate, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &Substrate{cli: cli}, nil
}

func (s *Substrate) Name() string { return "docker" }

func (s *Substrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{
		SharedKernel:    true,
		SupportsWindows: false,
		BootTime:        "ms",
		NICType:         "veth",
	}
}

// Ensure Substrate satisfies the interface at compile time.
var _ substrate.Substrate = (*Substrate)(nil)
```

- [ ] **Step 2: 写 image_test.go（TDD）**

`docker_test.go`:

```go
//go:build docker
// +build docker

package docker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestPrepareImagePulls(t *testing.T) {
	s, err := New()
	require.NoError(t, err)

	ref, err := s.PrepareImage(context.Background(), substrate.ImageSpec{
		DockerRef: "alpine:3.19",
	})
	require.NoError(t, err)
	require.NotEmpty(t, ref.ID)
	require.Equal(t, "alpine:3.19", ref.Repository)
}
```

- [ ] **Step 3: 运行确认失败（缺实现 + build tag 要加）**

```bash
go test -tags=docker ./pkg/provider/docker/... -v
```

Expected: FAIL with undefined `PrepareImage`.

- [ ] **Step 4: 实现 `image.go`**

```go
package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/image"

	"github.com/oslab/sysbox/pkg/substrate"
)

// PrepareImage pulls the given docker reference if not present locally.
func (s *Substrate) PrepareImage(ctx context.Context, spec substrate.ImageSpec) (substrate.ImageRef, error) {
	if spec.DockerRef == "" {
		return substrate.ImageRef{}, fmt.Errorf("docker substrate requires ImageSpec.DockerRef")
	}

	// Pull (idempotent if already present; reader MUST be drained).
	rc, err := s.cli.ImagePull(ctx, spec.DockerRef, image.PullOptions{})
	if err != nil {
		return substrate.ImageRef{}, fmt.Errorf("docker pull %s: %w", spec.DockerRef, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return substrate.ImageRef{}, fmt.Errorf("drain image pull: %w", err)
	}

	// Inspect to get the image ID.
	img, _, err := s.cli.ImageInspectWithRaw(ctx, spec.DockerRef)
	if err != nil {
		return substrate.ImageRef{}, fmt.Errorf("inspect image: %w", err)
	}

	return substrate.ImageRef{
		ID:         img.ID,
		Repository: spec.DockerRef,
	}, nil
}
```

- [ ] **Step 5: 运行测试确认通过**

```bash
go test -tags=docker ./pkg/provider/docker/... -run TestPrepareImage -v
```

Expected: PASS (需要 docker 可达 + 网络能 pull alpine).

- [ ] **Step 6: 提交**

```bash
git add pkg/provider/docker/docker.go pkg/provider/docker/image.go pkg/provider/docker/docker_test.go
git commit -m "feat(docker): implement Substrate.PrepareImage via docker pull"
```

---

## Task 8: Docker Substrate — 节点生命周期

**Files:**
- Create: `sysbox/pkg/provider/docker/node.go`
- Modify: `sysbox/pkg/provider/docker/docker_test.go`

- [ ] **Step 1: 扩展测试**

追加到 `docker_test.go`:

```go
func TestCreateStartStopDestroy(t *testing.T) {
	ctx := context.Background()
	s, err := New()
	require.NoError(t, err)

	ref, err := s.PrepareImage(ctx, substrate.ImageSpec{DockerRef: "alpine:3.19"})
	require.NoError(t, err)

	handle, err := s.CreateNode(ctx, substrate.NodeSpec{
		Name:  "sysbox-test-node",
		Image: ref,
	})
	require.NoError(t, err)
	require.NotEmpty(t, handle.ID)

	require.NoError(t, s.StartNode(ctx, handle))

	// Verify it's running via Exec.
	result, err := s.ExecInNode(ctx, handle, substrate.ExecSpec{
		Cmd: []string{"echo", "hello"},
	})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.Contains(t, result.Stdout, "hello")

	require.NoError(t, s.StopNode(ctx, handle))
	require.NoError(t, s.DestroyNode(ctx, handle))
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test -tags=docker ./pkg/provider/docker/... -run TestCreateStartStopDestroy -v
```

Expected: FAIL.

- [ ] **Step 3: 实现 `node.go`**

```go
package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	envs := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		envs = append(envs, fmt.Sprintf("%s=%s", k, v))
	}

	resp, err := s.cli.ContainerCreate(ctx,
		&container.Config{
			Image: spec.Image.Repository,
			Env:   envs,
			Cmd:   []string{"sleep", "infinity"}, // keep container alive for exec
		},
		&container.HostConfig{
			NetworkMode: "none", // we attach networks via AttachNIC later
			CapAdd:      []string{"NET_ADMIN"},
		},
		&network.NetworkingConfig{},
		nil,
		spec.Name,
	)
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("create container: %w", err)
	}

	return substrate.NodeHandle{
		ID: resp.ID,
		Attributes: map[string]any{
			"container_name": spec.Name,
		},
	}, nil
}

func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerStart(ctx, h.ID, container.StartOptions{})
}

func (s *Substrate) StopNode(ctx context.Context, h substrate.NodeHandle) error {
	timeoutSec := 10
	return s.cli.ContainerStop(ctx, h.ID, container.StopOptions{Timeout: &timeoutSec})
}

func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.cli.ContainerRemove(ctx, h.ID, container.RemoveOptions{Force: true})
}
```

- [ ] **Step 4: 实现 `exec.go`**

```go
package docker

import (
	"bytes"
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) ExecInNode(ctx context.Context, h substrate.NodeHandle, spec substrate.ExecSpec) (substrate.ExecResult, error) {
	envs := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		envs = append(envs, fmt.Sprintf("%s=%s", k, v))
	}

	ex, err := s.cli.ContainerExecCreate(ctx, h.ID, container.ExecOptions{
		Cmd:          spec.Cmd,
		Env:          envs,
		WorkingDir:   spec.WorkDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec create: %w", err)
	}

	att, err := s.cli.ContainerExecAttach(ctx, ex.ID, container.ExecStartOptions{})
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer att.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, att.Reader); err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec read: %w", err)
	}

	inspect, err := s.cli.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		return substrate.ExecResult{}, fmt.Errorf("exec inspect: %w", err)
	}

	return substrate.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: inspect.ExitCode,
	}, nil
}

// CopyToNode / CopyFromNode / AttachTTY: Phase 2 placeholder.
func (s *Substrate) CopyToNode(_ context.Context, _ substrate.NodeHandle, _, _ string) error {
	return fmt.Errorf("CopyToNode: not implemented in Phase 1")
}
func (s *Substrate) CopyFromNode(_ context.Context, _ substrate.NodeHandle, _, _ string) error {
	return fmt.Errorf("CopyFromNode: not implemented in Phase 1")
}

// AttachTTY and ObservationHook: Phase 2.
func (s *Substrate) AttachTTY(ctx context.Context, h substrate.NodeHandle) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("AttachTTY: not implemented in Phase 1")
}
```

**注意**: `AttachTTY` 的返回类型需要 `import "io"`。占位实现仍然要 compile。

- [ ] **Step 5: 实现 `ObservationHook` 占位**

追加到 `exec.go`:

```go
func (s *Substrate) ObservationHook(ctx context.Context, h substrate.NodeHandle) (substrate.ObservationTarget, error) {
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return substrate.ObservationTarget{}, err
	}
	return substrate.ObservationTarget{
		Kind:  "host-pid-namespace",
		Value: fmt.Sprintf("%d", ins.State.Pid),
	}, nil
}
```

- [ ] **Step 6: 运行测试确认通过**

```bash
go test -tags=docker ./pkg/provider/docker/... -v
```

Expected: all PASS.

- [ ] **Step 7: 提交**

```bash
git add pkg/provider/docker/
git commit -m "feat(docker): implement node lifecycle and exec"
```

---

## Task 9: Network Provider — netns 与 Bridge

**Files:**
- Create: `sysbox/pkg/provider/network/netns.go`
- Create: `sysbox/pkg/provider/network/bridge.go`
- Create: `sysbox/pkg/provider/network/network_test.go`

**前提**：测试需要 root 权限（netlink 操作）。用 build tag `netns` 隔离；CI 里 `sudo go test -tags=netns`。

- [ ] **Step 1: 写 netns 测试**

`network_test.go`:

```go
//go:build netns
// +build netns

package network

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateAndDeleteNetns(t *testing.T) {
	name := "sysbox-test-netns"

	require.NoError(t, CreateNetns(name))

	// Verify it's visible in /var/run/netns/
	require.FileExists(t, "/var/run/netns/"+name)

	require.NoError(t, DeleteNetns(name))
}
```

- [ ] **Step 2: 实现 `netns.go`**

```go
// Package network creates and wires network primitives for sysbox fields:
// network namespaces, Linux bridges, veth pairs, and nftables rules.
//
// All operations go through netlink; we don't shell out to iproute2.
package network

import (
	"fmt"
	"os"

	"github.com/vishvananda/netns"
)

// CreateNetns creates a new named network namespace at /var/run/netns/<name>.
func CreateNetns(name string) error {
	// netns.NewNamed() creates and enters the namespace. We want to create
	// but not enter, so do the equivalent manually: unshare CLONE_NEWNET in
	// a locked goroutine, bind-mount, then unlock.
	ns, err := netns.NewNamed(name)
	if err != nil {
		return fmt.Errorf("create netns %s: %w", name, err)
	}
	defer ns.Close()

	// Switch back to root netns; we only wanted to create.
	rootNS, err := netns.Get()
	if err != nil {
		return err
	}
	if err := netns.Set(rootNS); err != nil {
		return err
	}
	rootNS.Close()
	return nil
}

// DeleteNetns removes the named namespace.
func DeleteNetns(name string) error {
	if err := netns.DeleteNamed(name); err != nil {
		return fmt.Errorf("delete netns %s: %w", name, err)
	}
	return nil
}

// NetnsExists reports whether a named namespace exists.
func NetnsExists(name string) bool {
	_, err := os.Stat("/var/run/netns/" + name)
	return err == nil
}
```

- [ ] **Step 3: 运行测试（需 sudo）**

```bash
sudo -E go test -tags=netns ./pkg/provider/network/... -run TestCreateAndDeleteNetns -v
```

Expected: PASS.

- [ ] **Step 4: 写 bridge 测试**

追加到 `network_test.go`:

```go
func TestCreateBridgeWithIP(t *testing.T) {
	nsName := "sysbox-test-br-ns"
	require.NoError(t, CreateNetns(nsName))
	defer DeleteNetns(nsName)

	cfg := BridgeConfig{
		NetnsName: nsName,
		BridgeName: "br-dmz",
		CIDR:       "10.0.99.1/24",
	}

	require.NoError(t, CreateBridge(cfg))
	defer DeleteBridge(cfg)

	require.True(t, BridgeExists(nsName, "br-dmz"))
}
```

- [ ] **Step 5: 实现 `bridge.go`**

```go
package network

import (
	"fmt"
	"net"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type BridgeConfig struct {
	NetnsName  string // network namespace to create bridge in
	BridgeName string // e.g. "br-dmz"
	CIDR       string // e.g. "10.0.1.1/24" (bridge's IP as gateway)
}

// CreateBridge builds a Linux bridge inside the named netns and assigns it
// an IP (acts as the gateway for nodes attached to the network).
func CreateBridge(cfg BridgeConfig) error {
	return inNetns(cfg.NetnsName, func() error {
		la := netlink.NewLinkAttrs()
		la.Name = cfg.BridgeName

		br := &netlink.Bridge{LinkAttrs: la}
		if err := netlink.LinkAdd(br); err != nil {
			return fmt.Errorf("add bridge %s: %w", cfg.BridgeName, err)
		}

		addr, err := netlink.ParseAddr(cfg.CIDR)
		if err != nil {
			return fmt.Errorf("parse CIDR %s: %w", cfg.CIDR, err)
		}
		if err := netlink.AddrAdd(br, addr); err != nil {
			return fmt.Errorf("add IP to bridge: %w", err)
		}

		return netlink.LinkSetUp(br)
	})
}

func DeleteBridge(cfg BridgeConfig) error {
	return inNetns(cfg.NetnsName, func() error {
		link, err := netlink.LinkByName(cfg.BridgeName)
		if err != nil {
			return nil // already gone
		}
		return netlink.LinkDel(link)
	})
}

func BridgeExists(nsName, brName string) bool {
	exists := false
	_ = inNetns(nsName, func() error {
		if _, err := netlink.LinkByName(brName); err == nil {
			exists = true
		}
		return nil
	})
	return exists
}

// inNetns runs fn inside the named netns and switches back.
// Uses runtime.LockOSThread so the netns switch doesn't leak to other goroutines.
func inNetns(name string, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	orig, err := netns.Get()
	if err != nil {
		return err
	}
	defer orig.Close()

	ns, err := netns.GetFromName(name)
	if err != nil {
		return fmt.Errorf("get netns %s: %w", name, err)
	}
	defer ns.Close()

	if err := netns.Set(ns); err != nil {
		return fmt.Errorf("enter netns: %w", err)
	}
	defer netns.Set(orig)

	return fn()
}

// Helper: parse CIDR into IP + mask.
func parseCIDRHelper(cidr string) (net.IP, *net.IPNet, error) {
	return net.ParseCIDR(cidr)
}
```

- [ ] **Step 6: 运行全部测试确认通过**

```bash
sudo -E go test -tags=netns ./pkg/provider/network/... -v
```

Expected: PASS.

- [ ] **Step 7: 提交**

```bash
git add pkg/provider/network/netns.go pkg/provider/network/bridge.go pkg/provider/network/network_test.go
git commit -m "feat(network): netns and bridge provisioning via netlink"
```

---

## Task 10: Network Provider — veth Pair

**Files:**
- Create: `sysbox/pkg/provider/network/veth.go`
- Modify: `sysbox/pkg/provider/network/network_test.go`

- [ ] **Step 1: 追加测试**

```go
func TestCreateVethPair(t *testing.T) {
	nsName := "sysbox-test-veth-ns"
	require.NoError(t, CreateNetns(nsName))
	defer DeleteNetns(nsName)
	require.NoError(t, CreateBridge(BridgeConfig{
		NetnsName: nsName, BridgeName: "br0", CIDR: "10.0.99.1/24",
	}))

	pair, err := CreateVethPair(VethSpec{
		HostEnd:   "veth-host-01",
		GuestEnd:  "veth-guest-01",
		NetnsName: nsName,
		BridgeName: "br0",
		GuestIP:   "10.0.99.10/24",
		Gateway:   "10.0.99.1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, pair.HostEnd)

	require.NoError(t, DeleteVethPair(pair))
}
```

- [ ] **Step 2: 实现 `veth.go`**

```go
package network

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

type VethSpec struct {
	HostEnd    string // name of the host-side peer (lives in netns, attaches to bridge)
	GuestEnd   string // name of the guest-side peer (moves into container's netns)
	NetnsName  string // netns where host-end + bridge live
	BridgeName string // bridge to attach host-end
	GuestIP    string // IP to assign to guest-end, CIDR notation
	Gateway    string // default gateway (bridge IP)
}

type VethHandle struct {
	HostEnd  string
	GuestEnd string
	NetnsName string
}

// CreateVethPair creates a veth pair in the netns, attaches the host end
// to the bridge, and prepares the guest end (caller moves it into the
// container netns via substrate.AttachNIC).
func CreateVethPair(spec VethSpec) (VethHandle, error) {
	err := inNetns(spec.NetnsName, func() error {
		la := netlink.NewLinkAttrs()
		la.Name = spec.HostEnd

		veth := &netlink.Veth{
			LinkAttrs: la,
			PeerName:  spec.GuestEnd,
		}
		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("add veth pair: %w", err)
		}

		// Host end: attach to bridge, bring up.
		hostLink, err := netlink.LinkByName(spec.HostEnd)
		if err != nil {
			return err
		}
		br, err := netlink.LinkByName(spec.BridgeName)
		if err != nil {
			return err
		}
		if err := netlink.LinkSetMaster(hostLink, br.(*netlink.Bridge)); err != nil {
			return fmt.Errorf("attach host end to bridge: %w", err)
		}
		if err := netlink.LinkSetUp(hostLink); err != nil {
			return err
		}

		// Guest end: leave in netns for substrate.AttachNIC to move.
		return nil
	})
	if err != nil {
		return VethHandle{}, err
	}

	return VethHandle{
		HostEnd:   spec.HostEnd,
		GuestEnd:  spec.GuestEnd,
		NetnsName: spec.NetnsName,
	}, nil
}

// DeleteVethPair removes the pair. Deleting either end deletes both.
func DeleteVethPair(h VethHandle) error {
	return inNetns(h.NetnsName, func() error {
		link, err := netlink.LinkByName(h.HostEnd)
		if err != nil {
			return nil // already gone
		}
		return netlink.LinkDel(link)
	})
}
```

- [ ] **Step 3: 运行测试确认通过**

```bash
sudo -E go test -tags=netns ./pkg/provider/network/... -run TestCreateVethPair -v
```

Expected: PASS.

- [ ] **Step 4: 提交**

```bash
git add pkg/provider/network/veth.go pkg/provider/network/network_test.go
git commit -m "feat(network): veth pair creation attached to bridge"
```

---

## Task 11: Docker AttachNIC — 把 veth 塞进容器

**Files:**
- Create: `sysbox/pkg/provider/docker/nic.go`
- Modify: `sysbox/pkg/provider/docker/docker_test.go`

这一步需要 docker + netns 两种 tag，测试最复杂。分成小步走。

- [ ] **Step 1: 写 AttachNIC 测试**

```go
//go:build docker && netns
// +build docker,netns

package docker

// Test: create container + netns + bridge + veth pair, then AttachNIC into
// container. Verify container can ping the bridge IP.

func TestAttachNICEndToEnd(t *testing.T) {
	// ... (完整 setup 见实现步骤, 这里先写骨架)
}
```

**说明**：这个测试是 Phase 1 最复杂的集成点。单元测试先跳过，放到 Task 17 E2E 里一次验证。本任务只实现 AttachNIC 接口 + 简单编译测试。

- [ ] **Step 2: 实现 `nic.go`**

```go
package docker

import (
	"context"
	"fmt"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC moves the guest-end of a veth pair into the container's netns,
// renames it to eth<N>, assigns the IP, and sets the default gateway.
//
// Assumptions:
//   - nic.GuestEnd already exists in the host-side netns (from network provider)
//   - container is running (StartNode completed)
//   - nic.Kind == "veth"
func (s *Substrate) AttachNIC(ctx context.Context, h substrate.NodeHandle, nic substrate.NIC) error {
	if nic.Kind != "veth" {
		return fmt.Errorf("docker substrate only supports veth, got %q", nic.Kind)
	}

	// Get the container's netns PID.
	ins, err := s.cli.ContainerInspect(ctx, h.ID)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	containerPID := ins.State.Pid
	if containerPID == 0 {
		return fmt.Errorf("container %s is not running", h.ID)
	}

	return attachVethToContainer(nic, containerPID)
}

func attachVethToContainer(nic substrate.NIC, containerPID int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Step 1: find the guest-end link in current (host) netns.
	link, err := netlink.LinkByName(nic.GuestEnd)
	if err != nil {
		return fmt.Errorf("find veth guest end %s: %w", nic.GuestEnd, err)
	}

	// Step 2: move it into container's netns by PID.
	if err := netlink.LinkSetNsPid(link, containerPID); err != nil {
		return fmt.Errorf("move veth to container netns: %w", err)
	}

	// Step 3: enter container's netns and configure the link.
	origNS, err := netns.Get()
	if err != nil {
		return err
	}
	defer origNS.Close()

	containerNS, err := netns.GetFromPid(containerPID)
	if err != nil {
		return fmt.Errorf("get container netns: %w", err)
	}
	defer containerNS.Close()

	if err := netns.Set(containerNS); err != nil {
		return fmt.Errorf("enter container netns: %w", err)
	}
	defer netns.Set(origNS)

	// Rename guest-end to eth0 (convention).
	containerLink, err := netlink.LinkByName(nic.GuestEnd)
	if err != nil {
		return fmt.Errorf("find link after move: %w", err)
	}
	if err := netlink.LinkSetName(containerLink, "eth0"); err != nil {
		return fmt.Errorf("rename to eth0: %w", err)
	}

	// Re-fetch after rename.
	containerLink, err = netlink.LinkByName("eth0")
	if err != nil {
		return err
	}

	// Assign IP.
	addr, err := netlink.ParseAddr(nic.IP)
	if err != nil {
		return fmt.Errorf("parse IP %s: %w", nic.IP, err)
	}
	if err := netlink.AddrAdd(containerLink, addr); err != nil {
		return fmt.Errorf("assign IP: %w", err)
	}

	// Bring up.
	if err := netlink.LinkSetUp(containerLink); err != nil {
		return err
	}

	// Default route.
	if nic.Gateway != "" {
		_, defaultNet, _ := netlink.ParseIPNet("0.0.0.0/0")
		gwIP := netlink.ParseIP(nic.Gateway)
		route := &netlink.Route{
			LinkIndex: containerLink.Attrs().Index,
			Dst:       defaultNet,
			Gw:        gwIP,
		}
		if err := netlink.RouteAdd(route); err != nil {
			return fmt.Errorf("add default route: %w", err)
		}
	}

	return nil
}
```

**注意**：`netlink.ParseIP` 可能不存在（版本差异）；用 `net.ParseIP` 替代：

```go
import "net"
...
gwIP := net.ParseIP(nic.Gateway)
```

- [ ] **Step 3: 确认编译通过**

```bash
go build ./pkg/provider/docker/...
```

Expected: success.

- [ ] **Step 4: 提交**

```bash
git add pkg/provider/docker/nic.go
git commit -m "feat(docker): implement AttachNIC — move veth into container netns"
```

---

## Task 12: Runtime — Plan 计算

**Files:**
- Create: `sysbox/pkg/runtime/plan.go`
- Create: `sysbox/pkg/runtime/runtime_test.go`

**Plan 的核心操作**：把 `graph.Graph`（desired）和 `state.State`（current）对 diff，输出 ChangeList。

- [ ] **Step 1: 写 plan 测试**

```go
package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

func TestPlanAddsNewResources(t *testing.T) {
	g := graph.New()
	g.AddNode("sysbox_network", "dmz", nil)
	g.AddNode("sysbox_node", "web", []graph.Ref{{Type: "sysbox_network", Name: "dmz"}})

	s := &state.State{Version: 1}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Len(t, plan.Add, 2)
	require.Empty(t, plan.Destroy)
}

func TestPlanDetectsDestroys(t *testing.T) {
	g := graph.New() // empty graph = nothing desired

	s := &state.State{
		Version: 1,
		Resources: []state.Resource{
			{Type: "sysbox_node", Name: "orphan", Provider: "docker"},
		},
	}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Len(t, plan.Destroy, 1)
	require.Equal(t, "orphan", plan.Destroy[0].Name)
}

func TestPlanPassesThroughUnchanged(t *testing.T) {
	g := graph.New()
	g.AddNode("sysbox_network", "dmz", nil)

	s := &state.State{
		Version: 1,
		Resources: []state.Resource{
			{Type: "sysbox_network", Name: "dmz", Provider: "network", Instance: map[string]any{"netns": "sysbox-net-dmz"}},
		},
	}

	plan, err := ComputePlan(g, s)
	require.NoError(t, err)
	require.Empty(t, plan.Add)
	require.Empty(t, plan.Destroy)
	require.Len(t, plan.Unchanged, 1)
}
```

- [ ] **Step 2: 运行确认失败**

```bash
go test ./pkg/runtime/... -v
```

Expected: FAIL with undefined `ComputePlan` / `Plan`.

- [ ] **Step 3: 实现 `plan.go`**

```go
// Package runtime is the execution engine: computes plans by diffing
// the desired graph against the current state, and executes them by
// walking the graph and calling providers.
package runtime

import (
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

type Plan struct {
	Add       []graph.NodeID     // in graph, not in state → create
	Destroy   []state.Resource   // in state, not in graph → destroy
	Unchanged []graph.NodeID     // in both → no-op (Phase 1 treats all as unchanged)
	// Change   []graph.NodeID     // in both but diff — Phase 2 feature
}

// ComputePlan diffs the graph vs state.
// Phase 1 simplification: no "Change" detection; any resource present in
// both is Unchanged. Plan 2 adds drift detection.
func ComputePlan(g *graph.Graph, s *state.State) (*Plan, error) {
	p := &Plan{}

	inGraph := map[graph.NodeID]bool{}
	for _, n := range g.All() {
		inGraph[n.ID] = true
	}

	inState := map[graph.NodeID]bool{}
	for _, r := range s.Resources {
		inState[graph.NodeID{Type: r.Type, Name: r.Name}] = true
	}

	// Adds: in graph, not in state.
	for id := range inGraph {
		if !inState[id] {
			p.Add = append(p.Add, id)
		} else {
			p.Unchanged = append(p.Unchanged, id)
		}
	}

	// Destroys: in state, not in graph.
	for _, r := range s.Resources {
		id := graph.NodeID{Type: r.Type, Name: r.Name}
		if !inGraph[id] {
			p.Destroy = append(p.Destroy, r)
		}
	}

	return p, nil
}

func (p *Plan) HasChanges() bool {
	return len(p.Add) > 0 || len(p.Destroy) > 0
}

// Summary is a human-readable description of the plan.
func (p *Plan) Summary() string {
	return fmt.Sprintf("Plan: %d to add, %d to destroy, %d unchanged.",
		len(p.Add), len(p.Destroy), len(p.Unchanged))
}
```

(`fmt` import needed)

- [ ] **Step 4: 运行测试通过**

```bash
go test ./pkg/runtime/... -v
```

Expected: PASS.

- [ ] **Step 5: 提交**

```bash
git add pkg/runtime/plan.go pkg/runtime/runtime_test.go
git commit -m "feat(runtime): compute plan by diffing graph against state"
```

---

## Task 13: Runtime — Apply / Destroy 执行器

**Files:**
- Create: `sysbox/pkg/runtime/apply.go`
- Create: `sysbox/pkg/runtime/destroy.go`
- Create: `sysbox/pkg/runtime/executor.go`

**关键约定**：Executor 知道怎么把 graph node 类型（`sysbox_node`/`sysbox_network`/...）映射到正确的 substrate 或网络 provider 调用。这是"路由表"。

- [ ] **Step 1: 实现 `executor.go` — 资源类型 → 动作路由**

```go
package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Executor wires graph walking to provider calls. It holds references to
// registered substrates (via substrate.Get) and updates state after each action.
type Executor struct {
	graph *graph.Graph
	state *state.State
}

func NewExecutor(g *graph.Graph, s *state.State) *Executor {
	return &Executor{graph: g, state: s}
}

// CreateResource dispatches a node in the graph to the right provider
// and records the result in state.
func (e *Executor) CreateResource(ctx context.Context, id graph.NodeID) error {
	node := e.graph.Get(id.Type, id.Name)
	if node == nil {
		return fmt.Errorf("node %s not in graph", id)
	}

	switch id.Type {
	case "sysbox_network":
		return e.createNetwork(ctx, node)
	case "sysbox_image":
		return e.createImage(ctx, node)
	case "sysbox_node":
		return e.createNode(ctx, node)
	default:
		// Phase 1 scope: sysbox_firewall, sysbox_router, sysbox_ssh_access
		// are parsed by config but NOT applied here. Phase 2 adds them.
		// Return nil (skip silently) so HCL with these blocks still applies
		// the rest. Tests verify the skipped resources don't break the run.
		return nil
	}
}

// DestroyResource tears down a resource listed in state.
func (e *Executor) DestroyResource(ctx context.Context, r state.Resource) error {
	switch r.Type {
	case "sysbox_network":
		return e.destroyNetwork(ctx, r)
	case "sysbox_node":
		return e.destroyNode(ctx, r)
	case "sysbox_image":
		return nil // images survive destroy in Phase 1
	default:
		return fmt.Errorf("unhandled destroy for %q", r.Type)
	}
}

// -- networks --

func (e *Executor) createNetwork(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.NetworkConfig)
	if !ok {
		return fmt.Errorf("network %s: wrong data type", n.ID)
	}

	nsName := fmt.Sprintf("sysbox-net-%s", n.ID.Name)
	if err := network.CreateNetns(nsName); err != nil {
		return err
	}

	brName := fmt.Sprintf("br-%s", n.ID.Name)
	// Gateway IP = first usable in CIDR, e.g. 10.0.1.1/24 for cidr=10.0.1.0/24.
	gwCIDR, err := network.GatewayCIDR(cfg.CIDR)
	if err != nil {
		return err
	}
	if err := network.CreateBridge(network.BridgeConfig{
		NetnsName: nsName, BridgeName: brName, CIDR: gwCIDR,
	}); err != nil {
		return err
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_network",
		Name:     n.ID.Name,
		Provider: "network",
		Instance: map[string]any{
			"netns":   nsName,
			"bridge":  brName,
			"cidr":    cfg.CIDR,
			"gateway": gwCIDR,
		},
	})
	return nil
}

func (e *Executor) destroyNetwork(ctx context.Context, r state.Resource) error {
	nsName, _ := r.Instance["netns"].(string)
	brName, _ := r.Instance["bridge"].(string)
	_ = network.DeleteBridge(network.BridgeConfig{NetnsName: nsName, BridgeName: brName})
	_ = network.DeleteNetns(nsName)
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// -- images --

func (e *Executor) createImage(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.ImageConfig)
	if !ok {
		return fmt.Errorf("image %s: wrong data type", n.ID)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}

	ref, err := sub.PrepareImage(ctx, substrate.ImageSpec{
		DockerRef: cfg.DockerRef,
		Rootfs:    cfg.Rootfs,
		Size:      cfg.Size,
	})
	if err != nil {
		return err
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_image",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"image_id":   ref.ID,
			"repository": ref.Repository,
		},
	})
	return nil
}

// -- nodes --

func (e *Executor) createNode(ctx context.Context, n *graph.Node) error {
	cfg, ok := n.Data.(*config.NodeConfig)
	if !ok {
		return fmt.Errorf("node %s: wrong data type", n.ID)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return err
	}
	sub, err := substrate.Get(subName)
	if err != nil {
		return err
	}

	// Look up image ref from state.
	imageName, err := resolveImageRef(cfg.Image)
	if err != nil {
		return err
	}
	imgState := e.state.FindResource("sysbox_image", imageName)
	if imgState == nil {
		return fmt.Errorf("image %s not applied yet", imageName)
	}
	imgRef := substrate.ImageRef{
		ID:         imgState.Instance["image_id"].(string),
		Repository: imgState.Instance["repository"].(string),
	}

	handle, err := sub.CreateNode(ctx, substrate.NodeSpec{
		Name:  fmt.Sprintf("sysbox-%s", n.ID.Name),
		Image: imgRef,
		Env:   cfg.Env,
	})
	if err != nil {
		return err
	}

	if err := sub.StartNode(ctx, handle); err != nil {
		_ = sub.DestroyNode(ctx, handle)
		return err
	}

	// Attach NICs for each link.
	nics := []map[string]any{}
	for i, link := range cfg.Links {
		nic, err := e.wireLink(ctx, n.ID.Name, i, link)
		if err != nil {
			_ = sub.DestroyNode(ctx, handle)
			return err
		}
		if err := sub.AttachNIC(ctx, handle, nic); err != nil {
			return err
		}
		nics = append(nics, map[string]any{
			"host_end":  nic.HostEnd,
			"guest_end": nic.GuestEnd,
			"ip":        nic.IP,
		})
	}

	e.state.AddResource(state.Resource{
		Type:     "sysbox_node",
		Name:     n.ID.Name,
		Provider: subName,
		Instance: map[string]any{
			"container_id": handle.ID,
			"nics":         nics,
		},
	})
	return nil
}

func (e *Executor) destroyNode(ctx context.Context, r state.Resource) error {
	sub, err := substrate.Get(r.Provider)
	if err != nil {
		return err
	}
	handle := substrate.NodeHandle{ID: r.Instance["container_id"].(string)}
	_ = sub.StopNode(ctx, handle)
	if err := sub.DestroyNode(ctx, handle); err != nil {
		return err
	}
	// Destroy associated veths.
	if nics, ok := r.Instance["nics"].([]any); ok {
		for _, item := range nics {
			n, _ := item.(map[string]any)
			hostEnd, _ := n["host_end"].(string)
			_ = network.DeleteVethPair(network.VethHandle{HostEnd: hostEnd})
		}
	}
	e.state.RemoveResource(r.Type, r.Name)
	return nil
}

// wireLink creates a veth pair in the network's netns and returns the NIC
// spec for the substrate to attach.
func (e *Executor) wireLink(ctx context.Context, nodeName string, idx int, link config.LinkConfig) (substrate.NIC, error) {
	netName, err := resolveNetworkRef(link.Network)
	if err != nil {
		return substrate.NIC{}, err
	}
	netState := e.state.FindResource("sysbox_network", netName)
	if netState == nil {
		return substrate.NIC{}, fmt.Errorf("network %s not applied yet", netName)
	}

	hostEnd := fmt.Sprintf("vh-%s-%d", nodeName, idx)
	guestEnd := fmt.Sprintf("vg-%s-%d", nodeName, idx)

	pair, err := network.CreateVethPair(network.VethSpec{
		HostEnd:    hostEnd,
		GuestEnd:   guestEnd,
		NetnsName:  netState.Instance["netns"].(string),
		BridgeName: netState.Instance["bridge"].(string),
		GuestIP:    link.IP,
		Gateway:    link.Gateway,
	})
	if err != nil {
		return substrate.NIC{}, err
	}

	return substrate.NIC{
		Kind:     "veth",
		HostEnd:  pair.HostEnd,
		GuestEnd: pair.GuestEnd,
		IP:       link.IP,
		Gateway:  link.Gateway,
	}, nil
}

// -- reference resolution helpers --

// resolveSubstrateRef takes "substrate.docker.light" → "docker".
// Phase 1 simplification: type is the substrate name (we only have one alias per type).
// Phase 2 will look up alias → type via HCL EvalContext.
func resolveSubstrateRef(ref string) (string, error) {
	// For Phase 1, accept bare name ("docker") or "substrate.docker.X"
	// Extract the middle segment.
	parts := strings.Split(ref, ".")
	switch len(parts) {
	case 1:
		return parts[0], nil
	case 3:
		return parts[1], nil
	default:
		return "", fmt.Errorf("unexpected substrate ref %q", ref)
	}
}

func resolveImageRef(ref string) (string, error) {
	// "sysbox_image.alpine.id" → "alpine"
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("bad image ref %q", ref)
	}
	return parts[1], nil
}

func resolveNetworkRef(ref string) (string, error) {
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("bad network ref %q", ref)
	}
	return parts[1], nil
}
```

Remember to `import "strings"`.

- [ ] **Step 2: 实现 `apply.go`**

```go
package runtime

import (
	"context"
	"fmt"
)

// Apply walks the plan forward: create everything in Add in topo order.
func (e *Executor) Apply(ctx context.Context, plan *Plan) error {
	order, err := e.graph.TopoSort()
	if err != nil {
		return err
	}

	// Build a set for quick lookup of what to add.
	toAdd := map[string]bool{}
	for _, id := range plan.Add {
		toAdd[id.String()] = true
	}

	for _, id := range order {
		if !toAdd[id.String()] {
			continue
		}
		fmt.Printf("[apply] creating %s\n", id)
		if err := e.CreateResource(ctx, id); err != nil {
			return fmt.Errorf("create %s: %w", id, err)
		}
	}
	return nil
}
```

- [ ] **Step 3: 实现 `destroy.go`**

```go
package runtime

import (
	"context"
	"fmt"
)

// Destroy walks the plan reverse: tear down Destroy set in reverse topo order.
func (e *Executor) Destroy(ctx context.Context, plan *Plan) error {
	// Build a graph-less reverse order directly from state.
	// Resources in state were added in forward topo order, so reversing the
	// state slice gives us reverse-dependency destroy order.

	byID := map[string]bool{}
	for _, r := range plan.Destroy {
		byID[r.Type+"."+r.Name] = true
	}

	// Iterate state backwards.
	for i := len(e.state.Resources) - 1; i >= 0; i-- {
		r := e.state.Resources[i]
		if !byID[r.Type+"."+r.Name] {
			continue
		}
		fmt.Printf("[destroy] removing %s.%s\n", r.Type, r.Name)
		if err := e.DestroyResource(ctx, r); err != nil {
			return fmt.Errorf("destroy %s.%s: %w", r.Type, r.Name, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: 加一个 GatewayCIDR helper 到 network 包**

追加 `pkg/provider/network/bridge.go`:

```go
// GatewayCIDR takes "10.0.1.0/24" and returns "10.0.1.1/24" (first usable host).
func GatewayCIDR(cidr string) (string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	// Increment last octet by 1.
	ip[len(ip)-1]++
	return ip.String() + "/" + strings.Split(cidr, "/")[1], nil
}
```

(加 `"strings"` import)

- [ ] **Step 5: 确认编译**

```bash
go build ./...
```

Expected: success.

- [ ] **Step 6: 提交**

```bash
git add pkg/runtime/ pkg/provider/network/bridge.go
git commit -m "feat(runtime): apply and destroy executors with provider dispatch"
```

---

## Task 14: CLI — Root Command + Init

**Files:**
- Create: `sysbox/cmd/sysbox/main.go`（覆盖占位）
- Create: `sysbox/cmd/sysbox/commands/root.go`
- Create: `sysbox/cmd/sysbox/commands/init_cmd.go`

- [ ] **Step 1: 实现 `main.go`**

```go
package main

import (
	"fmt"
	"os"

	"github.com/oslab/sysbox/cmd/sysbox/commands"

	// Force-register substrates.
	docker "github.com/oslab/sysbox/pkg/provider/docker"

	"github.com/oslab/sysbox/pkg/substrate"
)

func main() {
	// Register built-in substrates. Phase 1: just docker.
	dockerSub, err := docker.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: docker substrate unavailable: %v\n", err)
	} else {
		substrate.Register(dockerSub)
	}

	if err := commands.Execute(); err != nil {
		os.Exit(1)
	}
}
```

- [ ] **Step 2: 实现 `commands/root.go`**

```go
package commands

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sysbox",
	Short: "AI 红队的 Terraform — Linux 攻防靶场 IaC",
}

var (
	flagConfigFile string // -f / --file
	flagStateFile  string // --state
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagConfigFile, "file", "f",
		"field.sysbox.hcl", "path to sysbox HCL config")
	rootCmd.PersistentFlags().StringVar(&flagStateFile, "state",
		"runs/default/state.json", "path to state file")

	rootCmd.AddCommand(initCmd, planCmd, applyCmd, destroyCmd, stateCmd, showCmd, outputCmd)
}

// Execute is called by main(). Returns an error so main() can set exit code.
func Execute() error {
	return rootCmd.Execute()
}
```

- [ ] **Step 3: 实现 `commands/init_cmd.go`**

```go
package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new sysbox workspace",
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	// Create the runs/ directory and an empty state file.
	stateDir := filepath.Dir(flagStateFile)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	fmt.Printf("Initialized sysbox workspace:\n")
	fmt.Printf("  config:  %s\n", flagConfigFile)
	fmt.Printf("  state:   %s\n", flagStateFile)
	return nil
}
```

- [ ] **Step 4: 构建 + 跑 init**

```bash
go build -o bin/sysbox ./cmd/sysbox
./bin/sysbox init
```

Expected output:
```
Initialized sysbox workspace:
  config:  field.sysbox.hcl
  state:   runs/default/state.json
```

- [ ] **Step 5: 提交**

```bash
git add cmd/sysbox/ 
git commit -m "feat(cli): add root command and init subcommand"
```

---

## Task 15: CLI — Plan / Apply / Destroy

**Files:**
- Create: `sysbox/cmd/sysbox/commands/plan_cmd.go`
- Create: `sysbox/cmd/sysbox/commands/apply_cmd.go`
- Create: `sysbox/cmd/sysbox/commands/destroy_cmd.go`
- Create: `sysbox/cmd/sysbox/commands/loader.go`（共享 helper 载入 graph+state）

- [ ] **Step 1: 实现 `loader.go`**（所有子命令共用的 HCL→graph、state load 逻辑）

```go
package commands

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

// loadWorkspace parses the HCL file into a graph and loads the state.
func loadWorkspace() (*graph.Graph, *state.Manager, *state.State, error) {
	root, err := config.ParseFile(flagConfigFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse config: %w", err)
	}

	g := graph.New()
	if err := buildGraph(root, g); err != nil {
		return nil, nil, nil, fmt.Errorf("build graph: %w", err)
	}

	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load state: %w", err)
	}
	return g, mgr, s, nil
}

// buildGraph converts parsed HCL into a resource DAG.
// Phase 1 extracts dependencies by parsing inner configs.
func buildGraph(root *config.Root, g *graph.Graph) error {
	for _, r := range root.Resources {
		var deps []graph.Ref
		var data any

		switch r.Type {
		case "sysbox_network":
			cfg := &config.NetworkConfig{}
			if err := config.DecodeResource(&r, cfg); err != nil {
				return err
			}
			data = cfg

		case "sysbox_image":
			cfg := &config.ImageConfig{}
			if err := config.DecodeResource(&r, cfg); err != nil {
				return err
			}
			data = cfg

		case "sysbox_node":
			cfg := &config.NodeConfig{}
			if err := config.DecodeResource(&r, cfg); err != nil {
				return err
			}
			data = cfg
			// image dep
			if imgName, err := resolveImageRef(cfg.Image); err == nil {
				deps = append(deps, graph.Ref{Type: "sysbox_image", Name: imgName})
			}
			// network deps (via links)
			for _, link := range cfg.Links {
				if netName, err := resolveNetworkRef(link.Network); err == nil {
					deps = append(deps, graph.Ref{Type: "sysbox_network", Name: netName})
				}
			}

		// Other types (firewall/router/ssh_access) simplified for Phase 1 demo.
		default:
			// Unknown types skipped silently in Phase 1; warn in stderr.
			continue
		}

		g.AddNode(r.Type, r.Name, deps)
		g.SetData(r.Type, r.Name, data)
	}
	return nil
}

// Duplicate of runtime resolvers — keep here to avoid circular import.
// Phase 2 cleanup: move to a shared pkg/ref/ package.
func resolveImageRef(ref string) (string, error) {
	parts := splitRef(ref)
	if len(parts) >= 2 {
		return parts[1], nil
	}
	return "", fmt.Errorf("bad image ref: %s", ref)
}

func resolveNetworkRef(ref string) (string, error) {
	parts := splitRef(ref)
	if len(parts) >= 2 {
		return parts[1], nil
	}
	return "", fmt.Errorf("bad network ref: %s", ref)
}

func splitRef(ref string) []string {
	var out []string
	cur := ""
	for _, c := range ref {
		if c == '.' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
```

- [ ] **Step 2: 实现 `plan_cmd.go`**

```go
package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Show changes sysbox would make without applying",
	RunE:  runPlan,
}

func runPlan(cmd *cobra.Command, args []string) error {
	g, _, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	plan, err := runtime.ComputePlan(g, s)
	if err != nil {
		return err
	}

	fmt.Println(plan.Summary())
	for _, id := range plan.Add {
		fmt.Printf("  + %s\n", id)
	}
	for _, r := range plan.Destroy {
		fmt.Printf("  - %s.%s\n", r.Type, r.Name)
	}
	for _, id := range plan.Unchanged {
		fmt.Printf("    %s (unchanged)\n", id)
	}
	return nil
}
```

- [ ] **Step 3: 实现 `apply_cmd.go`**

```go
package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply the plan: provision missing resources",
	RunE:  runApply,
}

func runApply(cmd *cobra.Command, args []string) error {
	g, mgr, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	plan, err := runtime.ComputePlan(g, s)
	if err != nil {
		return err
	}
	if !plan.HasChanges() {
		fmt.Println("No changes. Apply is a no-op.")
		return nil
	}

	fmt.Println(plan.Summary())

	exec := runtime.NewExecutor(g, s)
	if err := exec.Apply(context.Background(), plan); err != nil {
		// Save partial state before returning, so destroy can clean up.
		_ = mgr.Save(s)
		return fmt.Errorf("apply: %w", err)
	}

	if err := mgr.Save(s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	fmt.Println("Apply complete.")
	return nil
}
```

- [ ] **Step 4: 实现 `destroy_cmd.go`**

```go
package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/graph"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Tear down all resources in state",
	RunE:  runDestroy,
}

func runDestroy(cmd *cobra.Command, args []string) error {
	_, mgr, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	if len(s.Resources) == 0 {
		fmt.Println("Nothing to destroy.")
		return nil
	}

	// Treat destroy as "plan = all state resources should go away".
	plan := &runtime.Plan{Destroy: append([]state.Resource(nil), s.Resources...)}
	_ = graph.New() // destroy path doesn't need graph

	exec := runtime.NewExecutor(graph.New(), s)
	if err := exec.Destroy(context.Background(), plan); err != nil {
		_ = mgr.Save(s)
		return fmt.Errorf("destroy: %w", err)
	}

	if err := mgr.Save(s); err != nil {
		return err
	}
	fmt.Println("Destroy complete.")
	return nil
}
```

(需要 `import "github.com/oslab/sysbox/pkg/state"`)

- [ ] **Step 5: 构建并测试 plan 命令**

```bash
go build -o bin/sysbox ./cmd/sysbox
./bin/sysbox init
cat > field.sysbox.hcl <<'EOF'
substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "dmz" {
  cidr = "10.0.99.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = "docker"
  docker_ref = "alpine:3.19"
}

resource "sysbox_node" "a" {
  image     = "sysbox_image.alpine.id"
  substrate = "docker"
  links = [
    { network = "sysbox_network.dmz.id", ip = "10.0.99.10/24" },
  ]
}
EOF

./bin/sysbox plan
```

Expected output contains:
```
Plan: 3 to add, 0 to destroy, 0 unchanged.
  + sysbox_network.dmz
  + sysbox_image.alpine
  + sysbox_node.a
```

- [ ] **Step 6: 提交**

```bash
git add cmd/sysbox/commands/
git commit -m "feat(cli): add plan, apply, destroy subcommands"
```

---

## Task 16: CLI — State / Show / Output

**Files:**
- Create: `sysbox/cmd/sysbox/commands/state_cmd.go`
- Create: `sysbox/cmd/sysbox/commands/show_cmd.go`
- Create: `sysbox/cmd/sysbox/commands/output_cmd.go`

- [ ] **Step 1: 实现 `state_cmd.go`**

```go
package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var stateCmd = &cobra.Command{
	Use:   "state",
	Short: "Inspect the state file",
}

var stateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all resources currently in state",
	RunE:  runStateList,
}

func init() {
	stateCmd.AddCommand(stateListCmd)
}

func runStateList(cmd *cobra.Command, args []string) error {
	_, _, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	if len(s.Resources) == 0 {
		fmt.Println("(no resources)")
		return nil
	}

	for _, r := range s.Resources {
		fmt.Printf("%s.%s [provider=%s]\n", r.Type, r.Name, r.Provider)
	}
	return nil
}
```

- [ ] **Step 2: 实现 `show_cmd.go`**

```go
package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show <type.name>",
	Short: "Print resource details from state as JSON",
	Args:  cobra.ExactArgs(1),
	RunE:  runShow,
}

func runShow(cmd *cobra.Command, args []string) error {
	_, _, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	parts := splitRef(args[0])
	if len(parts) != 2 {
		return fmt.Errorf("expected type.name (e.g. sysbox_node.web), got %q", args[0])
	}

	r := s.FindResource(parts[0], parts[1])
	if r == nil {
		return fmt.Errorf("resource %s not found in state", args[0])
	}

	bytes, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(bytes))
	return nil
}
```

- [ ] **Step 3: 实现 `output_cmd.go`**

```go
package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var outputCmd = &cobra.Command{
	Use:   "output [resource.attribute]",
	Short: "Print outputs from state (JSON format, or a specific attribute)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runOutput,
}

func runOutput(cmd *cobra.Command, args []string) error {
	_, _, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		// Dump full state as JSON.
		bytes, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(bytes))
		return nil
	}

	// Parse "type.name.attr" or "type.name" (returns full instance).
	parts := splitRef(args[0])
	if len(parts) < 2 {
		return fmt.Errorf("expected type.name[.attr], got %q", args[0])
	}
	r := s.FindResource(parts[0], parts[1])
	if r == nil {
		return fmt.Errorf("resource %s.%s not found", parts[0], parts[1])
	}

	if len(parts) == 2 {
		bytes, _ := json.MarshalIndent(r.Instance, "", "  ")
		fmt.Println(string(bytes))
		return nil
	}

	val, ok := r.Instance[parts[2]]
	if !ok {
		return fmt.Errorf("attribute %s not found on %s.%s", parts[2], parts[0], parts[1])
	}
	fmt.Println(val)
	return nil
}
```

- [ ] **Step 4: 构建并手工验证**

```bash
go build -o bin/sysbox ./cmd/sysbox
./bin/sysbox state list
./bin/sysbox show sysbox_network.dmz     # 应失败: 尚未 apply
```

Expected: state list 打印 `(no resources)`；show 返回 "not found".

- [ ] **Step 5: 提交**

```bash
git add cmd/sysbox/commands/state_cmd.go cmd/sysbox/commands/show_cmd.go cmd/sysbox/commands/output_cmd.go
git commit -m "feat(cli): add state list / show / output subcommands"
```

---

## Task 17: 示例 HCL

**Files:**
- Create: `sysbox/examples/hello-world/field.sysbox.hcl`
- Create: `sysbox/examples/hello-world/README.md`

- [ ] **Step 1: 创建 `hello-world/field.sysbox.hcl`**

```hcl
# Hello World sysbox field
#
# Two Alpine containers on a shared bridge. Used by tests/e2e/helloworld_test.go
# to verify apply/destroy and inter-node connectivity.

substrate "docker" {
  alias = "light"
}

resource "sysbox_network" "lan" {
  cidr = "10.0.99.0/24"
}

resource "sysbox_image" "alpine" {
  substrate  = "docker"
  docker_ref = "alpine:3.19"
}

resource "sysbox_node" "node_a" {
  image     = "sysbox_image.alpine.id"
  substrate = "docker"
  links = [
    { network = "sysbox_network.lan.id", ip = "10.0.99.10/24", gw = "10.0.99.1" },
  ]
}

resource "sysbox_node" "node_b" {
  image     = "sysbox_image.alpine.id"
  substrate = "docker"
  links = [
    { network = "sysbox_network.lan.id", ip = "10.0.99.20/24", gw = "10.0.99.1" },
  ]
}
```

- [ ] **Step 2: 创建 `hello-world/README.md`**

```markdown
# Hello World Field

A minimal sysbox field with two Alpine containers on a shared bridge.

## Run

```bash
# From sysbox root
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state examples/hello-world/state.json init
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state examples/hello-world/state.json plan
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state examples/hello-world/state.json apply

# Verify connectivity (from node_a ping node_b)
docker exec sysbox-node_a ping -c 1 10.0.99.20

# Teardown
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state examples/hello-world/state.json destroy
```

**Note:** `sudo` is required because sysbox creates Linux network namespaces
and moves veth interfaces. `-E` preserves your DOCKER_HOST env.
```

- [ ] **Step 3: 提交**

```bash
git add examples/hello-world/
git commit -m "docs: add hello-world example field"
```

---

## Task 18: 端到端集成测试

**Files:**
- Create: `sysbox/tests/e2e/helloworld_test.go`

- [ ] **Step 1: 写 E2E 测试**

```go
//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHelloWorldField exercises the full apply→ping→destroy cycle.
// Requires: docker daemon running, root (for netns/veth).
func TestHelloWorldField(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	hclPath := filepath.Join(repoRoot, "examples/hello-world/field.sysbox.hcl")
	statePath := filepath.Join(repoRoot, "runs/e2e-helloworld/state.json")
	binPath := filepath.Join(repoRoot, "bin/sysbox")

	// Ensure binary is built.
	build := exec.Command("go", "build", "-o", binPath, "./cmd/sysbox")
	build.Dir = repoRoot
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)

	sysbox := func(args ...string) ([]byte, error) {
		full := append([]string{"-f", hclPath, "--state", statePath}, args...)
		cmd := exec.Command(binPath, full...)
		cmd.Dir = repoRoot
		return cmd.CombinedOutput()
	}

	// Always destroy at end so test is idempotent.
	t.Cleanup(func() {
		_, _ = sysbox("destroy")
	})

	// apply
	out, err = sysbox("init")
	require.NoError(t, err, "init: %s", out)

	out, err = sysbox("apply")
	require.NoError(t, err, "apply: %s", out)
	require.Contains(t, string(out), "Apply complete")

	// state list should show 4 resources (network + image + 2 nodes)
	out, err = sysbox("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "sysbox_network.lan")
	require.Contains(t, string(out), "sysbox_node.node_a")
	require.Contains(t, string(out), "sysbox_node.node_b")

	// verify connectivity: node_a pings node_b
	ping := exec.Command("docker", "exec", "sysbox-node_a",
		"ping", "-c", "1", "-W", "3", "10.0.99.20")
	pingOut, err := ping.CombinedOutput()
	require.NoError(t, err, "ping failed: %s", pingOut)
	require.Contains(t, string(pingOut), "1 packets transmitted, 1 received")

	// destroy
	out, err = sysbox("destroy")
	require.NoError(t, err, "destroy: %s", out)
	require.Contains(t, string(out), "Destroy complete")

	// state list should be empty now
	out, err = sysbox("state", "list")
	require.NoError(t, err)
	require.Contains(t, string(out), "(no resources)")
}

// Debug helper — unused, keeping here for future tests.
var _ = func() *bytes.Buffer { return new(bytes.Buffer) }
var _ = strings.TrimSpace
var _ = fmt.Sprintf
```

- [ ] **Step 2: 运行 E2E（需 sudo + docker）**

```bash
# 先保证 docker daemon running
docker info > /dev/null || { echo "docker not running"; exit 1; }

# 跑测试（需要 sudo 因为会动 netns/veth）
sudo -E go test -tags=e2e ./tests/e2e/... -v -timeout 5m -run TestHelloWorldField
```

Expected: PASS（大约 30-60 秒，含 docker pull alpine）.

如果失败，调试顺序：
1. `sudo ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state /tmp/s.json apply` 看手工 apply 报什么
2. `docker ps` 看容器是否在跑
3. `ip netns list` 看 netns 是否创建
4. `sudo ip netns exec sysbox-net-lan ip addr` 看 bridge 和 veth 是否 ok

- [ ] **Step 3: 提交**

```bash
git add tests/e2e/
git commit -m "test(e2e): add hello-world field end-to-end test"
```

---

## Task 19: README 完善 + 收尾

**Files:**
- Modify: `sysbox/README.md`
- Create: `sysbox/docs/development.md`

- [ ] **Step 1: 完善 README**

```markdown
# sysbox

> AI 红队的 Terraform —— 一键搭起 Linux 攻防战场。

**Status:** Phase 1 MVP — Docker container + linux-bridge topology management.

See [docs/specs/2026-05-07-sysbox-design.md](docs/specs/2026-05-07-sysbox-design.md) for the full design.

## Current capabilities (Phase 1)

- HCL declarative topology with substrate/node/network/image resources
- Docker substrate: create/start/stop/destroy containers, inject networks via veth
- linux-bridge networking: netns + bridge + veth + IP assignment + default gateway
- `sysbox init / plan / apply / destroy / state list / show / output` CLI
- State file with atomic save + file lock

**Not yet (Phase 2+):** sensors, cgroup sessions, prediction matcher, Firecracker, libvirt,
SSH access sugar, firewall rules, replay bundle.

## Requirements

- Linux kernel with netns support (any modern distro)
- Docker daemon running and reachable
- Go 1.22+
- Root/sudo when running `apply`/`destroy` (needed for netlink)

## Build

```bash
make build
# => bin/sysbox
```

## Quickstart (Hello World field)

```bash
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state runs/hello/state.json init
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state runs/hello/state.json apply
docker exec sysbox-node_a ping -c 1 10.0.99.20
sudo -E ./bin/sysbox -f examples/hello-world/field.sysbox.hcl --state runs/hello/state.json destroy
```

## Testing

```bash
make test                            # unit tests (no docker needed)
sudo -E make e2e                     # e2e test (requires docker + root)
go test -tags=docker ./pkg/provider/docker/...  # docker-specific unit tests
sudo -E go test -tags=netns ./pkg/provider/network/...  # netns-specific
```

## Project Layout

See [docs/development.md](docs/development.md) for directory layout, where
to find interfaces, and how to add a new substrate or provider.

## Roadmap

- Phase 2 (W5-8): Tracee sensor + cgroup v2 session + sshd wrapper + cgroup-fallback labeler
- Phase 3 (W9-12): Firecracker + libvirt substrates + guest-sensor + cross-node session propagation + Prediction Matcher + Match Report
- Phase 4 (W13-16): Validation suite + replay bundle + realism scorecard + docs
```

- [ ] **Step 2: 创建 `docs/development.md`**

```markdown
# sysbox Development Guide

## Project Layout

```
sysbox/
├── cmd/sysbox/          # CLI entry and subcommands
├── pkg/
│   ├── config/          # HCL schema + parser
│   ├── graph/           # Resource DAG
│   ├── state/           # State file
│   ├── runtime/         # Plan and Apply/Destroy executors
│   ├── substrate/       # Substrate interface (contract for node providers)
│   └── provider/
│       ├── docker/      # Docker substrate implementation
│       └── network/     # Linux network primitives (bridge/veth/netns)
├── examples/            # Example HCL files
└── tests/
    ├── e2e/             # End-to-end integration tests
    └── testdata/        # Shared fixtures
```

## Adding a New Substrate (future Phase 3)

1. Create `pkg/provider/<name>/<name>.go` implementing `substrate.Substrate`
2. Register it in `cmd/sysbox/main.go` alongside docker
3. Add e2e test in `tests/e2e/`
4. Update HCL schema to accept `substrate "name" { ... }` block

See `pkg/provider/docker/docker.go` as reference.

## Build Tags

| Tag | Purpose |
|---|---|
| (none) | Pure unit tests, safe in any CI |
| `docker` | Docker daemon required |
| `netns` | Linux network namespaces + root required |
| `e2e` | Full integration, docker + root |

## State File Lifecycle

- `sysbox apply` updates state after each resource, saves atomically via tmp+rename + flock
- `sysbox destroy` reverse-walks state, removes entries as it goes
- Corruption recovery: delete `runs/*/state.json` and re-apply (Phase 1 has no remote backend)

## Known Limitations (Phase 1)

- No HCL reference resolution (`substrate.docker.light` treated as bare string split);
  full HCL EvalContext arrives with Phase 2 sensor/session references
- `sysbox_firewall`, `sysbox_router`, `sysbox_ssh_access` parsed but not applied
- No drift detection (plan doesn't diff current field state against state file)
- Single-host only; remote runtime is Phase 4+
```

- [ ] **Step 3: 提交**

```bash
git add README.md docs/development.md
git commit -m "docs: add Phase 1 README and development guide"
```

- [ ] **Step 4: Tag v0.1.0**

```bash
git tag -a v0.1.0-phase1 -m "Phase 1 MVP: Hello World field"
git log --oneline
```

Expected: 约 20 次提交形成干净的历史。

---

## Phase 1 Done Checklist

- [ ] `make build` 在空机器上成功构建
- [ ] `make test` 通过（不需要 docker）
- [ ] `sudo -E make e2e` 通过
- [ ] `sudo -E ./bin/sysbox apply` 起两个 Alpine 容器、互 ping 成功
- [ ] `sudo -E ./bin/sysbox destroy` 干净清理所有资源（容器、veth、netns、bridge）
- [ ] `sysbox state list` / `show` / `output` 命令输出正确
- [ ] State file 原子写入，中断不破坏
- [ ] Task 提交次数 ≈ 20，每次提交只做一件事
- [ ] Phase 1 代码量大约 2500-3500 行 Go

---

## Phase 1 结束后，Phase 2 起步提示

Phase 2 的目标是"观测与 session 锚定"。进 Phase 2 前确认：

- Phase 1 的 Substrate 接口没有 API 破坏性改动
- Network provider 能稳定处理双节点拓扑
- State file 格式稳定（Phase 2 会在 `Instance` 里加 sensor metadata，但不改顶层结构）
- `sysbox_sensor`、`sysbox_ssh_access` 的 HCL schema 已在 config 里定义但未实施——Phase 2 Task 1 从这些开始

---

*Plan 完成时间预估：2 engineer × 4 weeks = 40 person-days，约对应 Phase 1 的 20 个 Task（每个 Task 约 2 person-days）。TDD 风格严格执行，每个 Task 一次提交。遇到 blocker 及时抛出，不要累积。*
