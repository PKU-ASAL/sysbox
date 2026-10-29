package firecracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func verifyJailedProcess(m *vmManifest, p processIdentity) error {
	j := m.Jail
	if j == nil || j.UID == 0 || j.RootInode == 0 || j.CgroupInode == 0 || p.BootID != j.BootID {
		return fmt.Errorf("VMM jail process identity mismatch")
	}
	// pivot_root detaches the executable's mount from the host tree: procfs may
	// display /firecracker. Compare the actual inode through procfs instead.
	actualExe, err := os.Stat(fmt.Sprintf("/proc/%d/exe", p.PID))
	if err != nil {
		return err
	}
	expectedExe, err := os.Stat(filepath.Join(j.Root, filepath.Base(j.Executable)))
	if err != nil {
		return err
	}
	if !os.SameFile(actualExe, expectedExe) {
		return fmt.Errorf("VMM executable inode mismatch")
	}
	args := map[string]string{}
	for i := 1; i+1 < len(p.Argv); i += 2 {
		args[p.Argv[i]] = p.Argv[i+1]
	}
	if args["--id"] != m.VMID || args["--config-file"] != "/vm_config.json" || args["--api-sock"] != "/run/firecracker.sock" {
		return fmt.Errorf("VMM jail arguments mismatch")
	}
	proc := fmt.Sprintf("/proc/%d", p.PID)
	root, err := os.Stat(filepath.Join(proc, "root"))
	if err != nil {
		return err
	}
	expected, err := os.Stat(j.Root)
	if err != nil {
		return err
	}
	if !os.SameFile(root, expected) {
		return fmt.Errorf("VMM did not enter the recorded jail root")
	}
	for _, ns := range []string{"mnt", "pid", "ipc", "uts"} {
		actual, err := os.Readlink(filepath.Join(proc, "ns", ns))
		if err != nil {
			return err
		}
		host, err := os.Readlink(filepath.Join("/proc/self/ns", ns))
		if err != nil {
			return err
		}
		if actual == host {
			return fmt.Errorf("VMM %s namespace is not isolated", ns)
		}
	}
	netPath := "/proc/self/ns/net"
	if m.Handle.NetnsName != "" {
		netPath = filepath.Join("/run/netns", m.Handle.NetnsName)
	}
	actualNet, err := os.Stat(filepath.Join(proc, "ns/net"))
	if err != nil {
		return err
	}
	expectedNet, err := os.Stat(netPath)
	if err != nil {
		return err
	}
	sharedNetwork := m.Handle.NetnsName != "" || len(m.Handle.Attachments) > 0
	if sharedNetwork == !os.SameFile(actualNet, expectedNet) {
		return fmt.Errorf("VMM network namespace mismatch")
	}
	raw, err := os.ReadFile(filepath.Join(proc, "status"))
	if err != nil {
		return err
	}
	values := map[string][]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			values[strings.TrimSuffix(fields[0], ":")] = fields[1:]
		}
	}
	uid := strconv.FormatUint(uint64(j.UID), 10)
	for _, name := range []string{"Uid", "Gid"} {
		v := values[name]
		if len(v) != 4 {
			return fmt.Errorf("missing VMM credential evidence")
		}
		for _, s := range v {
			if s != uid {
				return fmt.Errorf("VMM did not drop %s", name)
			}
		}
	}
	for _, name := range []string{"CapEff", "CapPrm", "CapAmb"} {
		v := values[name]
		if len(v) != 1 || v[0] != "0000000000000000" {
			return fmt.Errorf("VMM retains %s", name)
		}
	}
	if v := values["Seccomp"]; len(v) != 1 || v[0] != "2" {
		return fmt.Errorf("VMM seccomp filtering is not active")
	}
	if v := values["NoNewPrivs"]; len(v) != 1 || v[0] != "1" {
		return fmt.Errorf("VMM no_new_privs is not set")
	}
	raw, err = os.ReadFile(filepath.Join(proc, "cgroup"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "0::/"+j.Cgroup {
		return fmt.Errorf("VMM cgroup differs from recorded jail")
	}
	return nil
}

func recoverJailLaunch(m *vmManifest) error {
	j := m.Jail
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	if boot != j.BootID {
		return fmt.Errorf("jail launch host boot changed; recovery required")
	}
	empty, err := jailCgroupEmpty(j)
	if err != nil {
		return err
	}
	if empty {
		m.Phase = "prepared"
		m.Process = processIdentity{}
		m.Handle.PID = 0
		m.Handle.PIDStart = ""
		return saveManifest(m)
	}
	raw, err := os.ReadFile(filepath.Join(jailCgroup(j), "cgroup.procs"))
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 1 {
		return fmt.Errorf("jail launch has %d processes; identity unresolved", len(fields))
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return err
	}
	p, err := captureProcessIdentity(pid)
	if err != nil {
		return err
	}
	if err = verifyJailedProcess(m, p); err != nil {
		return err
	}
	owns, err := processOwnsSocket(pid, "/run/firecracker.sock")
	if err != nil {
		return err
	}
	if !owns {
		return fmt.Errorf("VMM does not own its jail API socket")
	}
	m.Process = p
	m.Phase = "running"
	m.Handle.PID = p.PID
	m.Handle.PIDStart = p.StartTime
	return saveManifest(m)
}
