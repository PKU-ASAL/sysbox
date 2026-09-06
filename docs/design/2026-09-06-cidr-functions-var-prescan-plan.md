# CIDR 内置函数 + var 预扫描修复 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修掉「能力预扫描遇到 `var.<name>` 引用就报错」的 bug，并给 HCL 加 `cidrsubnet`/`cidrhost` 两个类型化网段函数。

**Architecture:** 能力预扫描（`requiredCapabilitiesForTopology` / `requiredCapabilitiesForNode`）不该完整解码 resource，只该解它关心的关键字段（`substrate` / `NAT`），用带 `hcl:",remain"` 的最小 probe 让其余字段（含 `var` 引用）不求值。CIDR 函数复用 `net/netip` 做 IPv4 子网计算，注册进现有 eval context 的 `Functions`。

**Tech Stack:** Go、hashicorp/hcl/v2 + gohcl、zclconf/go-cty、net/netip、testify。

---

## 文件结构

**修改：**
- `pkg/api/scheduler.go` —— 能力预扫描改为最小 probe；删除 `decodeCapabilityResource`。
- `pkg/api/scheduler_test.go` —— 新增回归测试。
- `pkg/config/eval.go` —— 三处 `Functions` map 注册新函数。

**新增：**
- `pkg/config/cidr_functions.go` —— `cidrsubnet` / `cidrhost` 函数定义 + IPv4 计算 helper。
- `pkg/config/cidr_functions_test.go` —— 函数单元测试 + 一个集成测试。

---

## Task 1: 能力预扫描改为最小 probe

**Files:**
- Modify: `pkg/api/scheduler.go:28-141`
- Test: `pkg/api/scheduler_test.go`

- [ ] **Step 1: 写失败的测试**

在 `pkg/api/scheduler_test.go` 末尾（`TestRequiredCapabilitiesForTopology` 之后）加：

```go
func TestRequiredCapabilitiesForTopologyIgnoresVarFields(t *testing.T) {
	dir := t.TempDir()
	hcl := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(hcl, []byte(`
variable "subnet_prefix" {}

resource "sysbox_network" "lab" {
  cidr = "${var.subnet_prefix}.0/24"
}

resource "sysbox_node" "web" {
  substrate = "docker"
  image     = "alpine"
  env       = { FLAG = var.subnet_prefix }
}
`), 0o644))

	caps, err := requiredCapabilitiesForTopology(hcl)
	require.NoError(t, err)
	require.Contains(t, caps, "network")
	require.Contains(t, caps, "docker")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/api/ -run TestRequiredCapabilitiesForTopologyIgnoresVarFields -v`
Expected: FAIL，报 `There is no variable named var`（完整 decode 了 `cidr = "${var.subnet_prefix}.0/24"` 里的 var 引用）

- [ ] **Step 3: 实现最小 probe**

在 `pkg/api/scheduler.go`，把 `requiredCapabilitiesForTopology`（28-63）和 `requiredCapabilitiesForNode`（65-92）替换为以下实现，并删除 `decodeCapabilityResource`（94-141）：

```go
// substrateProbe 只解能力预扫描关心的 substrate 字段；其余字段（含 var.<name>
// 引用，如 cidr/env）进 Remain 不求值，这样预扫描不会因为不关心的字段引用 var
// 而失败。
type substrateProbe struct {
	Substrate string   `hcl:"substrate"`
	Remain    hcl.Body `hcl:",remain"`
}

// networkProbe 只解能力预扫描关心的 NAT 字段。
type networkProbe struct {
	NAT    bool     `hcl:"nat,optional"`
	Remain hcl.Body `hcl:",remain"`
}

func requiredCapabilitiesForTopology(path string) ([]string, error) {
	root, err := config.ParseFile(path)
	if err != nil {
		return nil, err
	}
	evalCtx, err := config.BuildEvalContext(root)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range root.Resources {
		switch r.Type {
		case "sysbox_node", "sysbox_router", "sysbox_image", "sysbox_kernel":
			probe := &substrateProbe{}
			if err := config.DecodeResource(&r, probe, evalCtx); err != nil {
				return nil, err
			}
			addSubstrateCapabilities(set, probe.Substrate)
		case "sysbox_network":
			probe := &networkProbe{}
			if err := config.DecodeResource(&r, probe, evalCtx); err != nil {
				return nil, err
			}
			if !probe.NAT {
				set["network"] = true
			}
		case "sysbox_firewall", "sysbox_ssh_access":
			set["network"] = true
		}
	}
	return capabilitiesFromSet(set), nil
}

func requiredCapabilitiesForNode(path, node string) ([]string, error) {
	root, err := config.ParseFile(path)
	if err != nil {
		return nil, err
	}
	evalCtx, err := config.BuildEvalContext(root)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, r := range root.Resources {
		if r.Name != node || (r.Type != "sysbox_node" && r.Type != "sysbox_router") {
			continue
		}
		probe := &substrateProbe{}
		if err := config.DecodeResource(&r, probe, evalCtx); err != nil {
			return nil, err
		}
		addSubstrateCapabilities(set, probe.Substrate)
		return capabilitiesFromSet(set), nil
	}
	return nil, fmt.Errorf("node %q not found in topology", node)
}
```

（`config.DecodeResource` 内部的 `validateLogicalAttachmentNames` 对 probe 走 default 分支返回 nil，不会受影响。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./pkg/api/ -run TestRequiredCapabilitiesForTopology -v`
Expected: 两个测试（含原有 `TestRequiredCapabilitiesForTopology` 和新增 `...IgnoresVarFields`）都 PASS

- [ ] **Step 5: 全量确认无回归**

Run: `go build ./... && go test ./pkg/api/... -count=1`
Expected: `ok github.com/oslab/sysbox/pkg/api`

- [ ] **Step 6: 提交**

```bash
git add pkg/api/scheduler.go pkg/api/scheduler_test.go
git commit -m "fix(api): 能力预扫描只解关键字段，不再因 var 引用失败"
```

---

## Task 2: `cidrsubnet` / `cidrhost` 内置函数

**Files:**
- Create: `pkg/config/cidr_functions.go`
- Modify: `pkg/config/eval.go`（三处 Functions map）
- Test: `pkg/config/cidr_functions_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `pkg/config/cidr_functions_test.go`：

```go
package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestCidrsubnet(t *testing.T) {
	cases := []struct {
		prefix  string
		newbits int64
		netnum  int64
		want    string
	}{
		{"10.200.0.0/16", 8, 0, "10.200.0.0/24"},
		{"10.200.0.0/16", 8, 1, "10.200.1.0/24"},
		{"10.200.0.0/16", 8, 99, "10.200.99.0/24"},
		{"10.1.2.3/16", 8, 0, "10.1.0.0/24"}, // 先 Masked 规范化
	}
	for _, c := range cases {
		got, err := cidrsubnetFunc.Call([]cty.Value{
			cty.StringVal(c.prefix), cty.NumberIntVal(c.newbits), cty.NumberIntVal(c.netnum),
		})
		require.NoError(t, err, c.prefix)
		require.Equal(t, c.want, got.AsString(), c.prefix)
	}
}

func TestCidrsubnetErrors(t *testing.T) {
	for _, c := range [][]cty.Value{
		{cty.StringVal("not-a-cidr"), cty.NumberIntVal(8), cty.NumberIntVal(0)},
		{cty.StringVal("10.0.0.0/16"), cty.NumberIntVal(17), cty.NumberIntVal(0)}, // 16+17>32
		{cty.StringVal("10.0.0.0/16"), cty.NumberIntVal(8), cty.NumberIntVal(256)}, // netnum 超界
	} {
		_, err := cidrsubnetFunc.Call(c)
		require.Error(t, err)
	}
}

func TestCidrhost(t *testing.T) {
	cases := []struct {
		prefix  string
		hostnum int64
		want    string
	}{
		{"10.200.0.0/24", 0, "10.200.0.0"},
		{"10.200.0.0/24", 1, "10.200.0.1"},
		{"10.200.0.0/24", 10, "10.200.0.10"},
	}
	for _, c := range cases {
		got, err := cidrhostFunc.Call([]cty.Value{
			cty.StringVal(c.prefix), cty.NumberIntVal(c.hostnum),
		})
		require.NoError(t, err, c.prefix)
		require.Equal(t, c.want, got.AsString(), c.prefix)
	}
}

func TestCidrhostErrors(t *testing.T) {
	for _, c := range [][]cty.Value{
		{cty.StringVal("bad"), cty.NumberIntVal(0)},
		{cty.StringVal("10.0.0.0/24"), cty.NumberIntVal(256)}, // hostnum 超界
	} {
		_, err := cidrhostFunc.Call(c)
		require.Error(t, err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./pkg/config/ -run 'TestCidrsubnet|TestCidrhost' -v`
Expected: FAIL，`cidrsubnetFunc` / `cidrhostFunc` 未定义

- [ ] **Step 3: 实现函数**

创建 `pkg/config/cidr_functions.go`：

```go
package config

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// cidrsubnetFunc implements cidrsubnet(prefix, newbits, netnum) → subnet CIDR
// string (IPv4 only), Terraform-compatible.
var cidrsubnetFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
		{Name: "newbits", Type: cty.Number},
		{Name: "netnum", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		prefix, err := parseIPv4Prefix(args[0].AsString())
		if err != nil {
			return cty.NilVal, err
		}
		newbits, _ := args[1].AsBigFloat().Int64()
		netnum, _ := args[2].AsBigFloat().Int64()
		if newbits < 0 {
			return cty.NilVal, fmt.Errorf("newbits must be non-negative")
		}
		newLen := prefix.Bits() + int(newbits)
		if newLen > 32 {
			return cty.NilVal, fmt.Errorf("prefix extension would exceed 32 bits")
		}
		if netnum < 0 || netnum >= (int64(1)<<uint(newbits)) {
			return cty.NilVal, fmt.Errorf("subnet number out of range")
		}
		return cty.StringVal(netip.PrefixFrom(subnetAddr(prefix, int(newbits), int(netnum)), newLen).String()), nil
	},
})

// cidrhostFunc implements cidrhost(prefix, hostnum) → host IP string (IPv4
// only), Terraform-compatible.
var cidrhostFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
		{Name: "hostnum", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		prefix, err := parseIPv4Prefix(args[0].AsString())
		if err != nil {
			return cty.NilVal, err
		}
		hostnum, _ := args[1].AsBigFloat().Int64()
		if hostnum < 0 || hostnum >= (int64(1)<<uint(32-prefix.Bits())) {
			return cty.NilVal, fmt.Errorf("host number out of range")
		}
		return cty.StringVal(hostAddr(prefix, int(hostnum)).String()), nil
	},
})

// parseIPv4Prefix parses an IPv4 prefix and normalizes it to its network
// address (Masked). IPv6 is rejected.
func parseIPv4Prefix(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR prefix")
	}
	if !prefix.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("IPv6 not supported")
	}
	return prefix.Masked(), nil
}

func subnetAddr(prefix netip.Prefix, newbits, netnum int) netip.Addr {
	subnetSize := uint32(1) << uint(32-prefix.Bits()-newbits)
	return fromU32(baseU32(prefix) + uint32(netnum)*subnetSize)
}

func hostAddr(prefix netip.Prefix, hostnum int) netip.Addr {
	return fromU32(baseU32(prefix) + uint32(hostnum))
}

func baseU32(prefix netip.Prefix) uint32 {
	return binary.BigEndian.Uint32(prefix.Addr().As4()[:])
}

func fromU32(v uint32) netip.Addr {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return netip.AddrFrom4(b)
}
```

- [ ] **Step 4: 注册到 eval context**

在 `pkg/config/eval.go` 的**三处** `Functions` map 里加 `"cidrsubnet": cidrsubnetFunc, "cidrhost": cidrhostFunc`：

三处原文都是：

```go
Functions: map[string]function.Function{"env": envFunc, "env_optional": envOptionalFunc, "toset": tosetFunc},
```

（第 65、90 行的 `preCtx`/`localCtx` 内联写，第 163 行 `ctx` 用多行）统一改成：

```go
Functions: map[string]function.Function{"env": envFunc, "env_optional": envOptionalFunc, "toset": tosetFunc, "cidrsubnet": cidrsubnetFunc, "cidrhost": cidrhostFunc},
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./pkg/config/ -run 'TestCidrsubnet|TestCidrhost' -v`
Expected: 全部 PASS

- [ ] **Step 6: 加集成测试（HCL 里用 cidrsubnet 派生 cidr）**

在 `pkg/config/cidr_functions_test.go` 末尾加：

```go
func TestCidrsubnetInHCL(t *testing.T) {
	root, err := ParseString(`
resource "sysbox_network" "lab" {
  cidr = cidrsubnet("10.200.0.0/16", 8, 99)
}
`, "test.hcl")
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	var cfg NetworkConfig
	require.NoError(t, DecodeResource(&root.Resources[0], &cfg, ctx))
	require.Equal(t, "10.200.99.0/24", cfg.CIDR)
}
```

- [ ] **Step 7: 全量确认无回归**

Run: `go build ./... && go test ./pkg/config/... -count=1`
Expected: `ok github.com/oslab/sysbox/pkg/config`

- [ ] **Step 8: 提交**

```bash
git add pkg/config/cidr_functions.go pkg/config/cidr_functions_test.go pkg/config/eval.go
git commit -m "feat(config): 加 cidrsubnet / cidrhost 内置函数（IPv4）"
```

---

## Self-Review

**Spec 覆盖：**
- 交付 1（最小 probe 只解关键字段）→ Task 1 ✓
- 交付 2（cidrsubnet/cidrhost 签名/语义/错误处理/测试）→ Task 2 ✓
- 「不采用 unknown 占位」「无需传 inputs」→ 方案 A 实现中体现（签名不变）✓
- 「不做 IPv6」→ `parseIPv4Prefix` 拒绝 IPv6 ✓

**占位符扫描：** 无 TBD/TODO；每个 step 有完整代码或明确命令。

**类型一致性：** `substrateProbe`/`networkProbe` 在 Task 1 定义并使用；`cidrsubnetFunc`/`cidrhostFunc` 在 Task 2 Step 3 定义、Step 4 注册、Step 1/6 测试引用，一致。
