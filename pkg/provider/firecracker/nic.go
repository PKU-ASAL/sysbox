package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC creates a TAP device, connects it to the network's bridge,
// and records the NIC in the VM config JSON. The NIC will be available
// when StartNode launches the Firecracker process.
//
// Firecracker does NOT support NIC hot-plug in config-file mode,
// so we must declare all interfaces before boot.
func (s *Substrate) AttachNIC(ctx context.Context, h substrate.NodeHandle, req substrate.LinkRequest) (substrate.AttachedNIC, error) {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil || hs.ConfigPath == "" {
		return substrate.AttachedNIC{}, fmt.Errorf("VM config path not found in handle provider state")
	}

	tapName := fmt.Sprintf("tap-%s", strings.TrimPrefix(h.ID, "sysbox-"))
	if len(tapName) > 15 {
		tapName = tapName[:15]
	}

	netnsName := req.NetNS
	bridgeName := req.Bridge

	// Create or reuse the TAP device.
	tapInRoot := linkExists(tapName)
	tapInNetns := netnsName != "" && linkExistsInNetns(tapName, netnsName)

	if !tapInRoot && !tapInNetns {
		if err := createTapDevice(tapName); err != nil {
			return substrate.AttachedNIC{}, fmt.Errorf("create tap %s: %w", tapName, err)
		}
	} else {
		if tapInRoot {
			exec.Command(ipBin, "link", "set", tapName, "up").Run() //nolint:errcheck
		} else if tapInNetns {
			exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "link", "set", tapName, "up").Run() //nolint:errcheck
		}
	}

	// Attach TAP to the network bridge.
	if netnsName != "" && bridgeName != "" {
		if err := attachTapToBridge(tapName, bridgeName, netnsName); err != nil {
			return substrate.AttachedNIC{}, fmt.Errorf("attach tap to bridge: %w", err)
		}
		vmMu.Lock()
		if vm, ok := vmStore[h.ID]; ok && vm.netnsName == "" {
			vm.netnsName = netnsName
		}
		vmMu.Unlock()
		if hs.NetnsName == "" {
			hs.NetnsName = netnsName
		}
	}

	// Add NIC to the VM config JSON.
	cfgPath := hs.ConfigPath
	nicIdx := hs.NICCount
	ifaceID := fmt.Sprintf("eth%d", nicIdx)
	fcIface := fcNetworkInterface{
		IfaceID:     ifaceID,
		HostDevName: tapName,
	}
	if req.MAC != "" {
		fcIface.GuestMAC = req.MAC
	}

	if err := appendNICtoConfig(cfgPath, fcIface); err != nil {
		return substrate.AttachedNIC{}, fmt.Errorf("append NIC to config: %w", err)
	}

	// Phase A: kernel cmdline IP autoconfig for the first interface.
	if nicIdx == 0 && req.IP != "" {
		hostname := strings.TrimPrefix(h.ID, "sysbox-")
		if err := injectKernelIPArg(cfgPath, ifaceID, hostname, req.IP, req.Gateway); err != nil {
			return substrate.AttachedNIC{}, fmt.Errorf("inject kernel ip= arg: %w", err)
		}
	}

	hs.NICCount = nicIdx + 1
	hs.TapName = tapName

	return substrate.AttachedNIC{
		Kind:    substrate.NICKindTap,
		HostEnd: tapName,
		IP:      req.IP,
		NetNS:   netnsName,
	}, nil
}

// injectKernelIPArg rewrites the VM config's boot_args to include a kernel
// IP-autoconfig directive for the given interface. Idempotent: if an ip=
// directive is already present, it is replaced.
//
// Format (kernel docs Documentation/admin-guide/nfs/nfsroot.rst):
//
//	ip=<client-ip>::<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>
//
// We always set autoconf=off so the kernel does not try DHCP afterwards.
func injectKernelIPArg(cfgPath, dev, hostname, cidr, gw string) error {
	clientIP, mask, err := splitCIDR(cidr)
	if err != nil {
		return err
	}

	ipArg := fmt.Sprintf("ip=%s::%s:%s:%s:%s:off", clientIP, gw, mask, hostname, dev)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var cfg fcConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	cfg.BootSource.BootArgs = upsertCmdlineArg(cfg.BootSource.BootArgs, "ip", ipArg)

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(cfgPath, out, 0644)
}

// upsertCmdlineArg replaces the first token whose key matches `key=` with the
// fully-formed `kv` token, or appends kv if no such token exists.
// Preserves order of other tokens.
func upsertCmdlineArg(cmdline, key, kv string) string {
	prefix := key + "="
	tokens := strings.Fields(cmdline)
	replaced := false
	for i, t := range tokens {
		if strings.HasPrefix(t, prefix) {
			tokens[i] = kv
			replaced = true
			break
		}
	}
	if !replaced {
		tokens = append(tokens, kv)
	}
	return strings.Join(tokens, " ")
}

// splitCIDR splits "10.0.12.20/24" into ("10.0.12.20", "255.255.255.0").
func splitCIDR(cidr string) (string, string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("parse cidr %s: %w", cidr, err)
	}
	mask := net.IP(ipnet.Mask).To4()
	if mask == nil {
		return "", "", fmt.Errorf("only IPv4 supported, got %s", cidr)
	}
	return ip.String(), mask.String(), nil
}

// appendNICtoConfig reads the VM config JSON, appends a network interface,
// and writes it back.
func appendNICtoConfig(cfgPath string, iface fcNetworkInterface) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg fcConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	cfg.NetworkInterfaces = append(cfg.NetworkInterfaces, iface)

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(cfgPath, out, 0644)
}

// ipBin is the absolute path to the ip binary, resolved once at init.
var ipBin string

func init() {
	ipBin, _ = exec.LookPath("ip")
	if ipBin == "" {
		// Fallback to common locations.
		for _, p := range []string{"/sbin/ip", "/usr/sbin/ip", "/usr/bin/ip"} {
			if _, err := os.Stat(p); err == nil {
				ipBin = p
				break
			}
		}
	}
	if ipBin == "" {
		ipBin = "ip" // last resort
	}
}

// cleanupTap removes a TAP device from the specified netns (or root netns if empty).
// This handles the common case where a previous failed apply left the TAP
// inside a network netns — deleting from root netns would fail silently.
func cleanupTap(name, netnsName string) {
	if netnsName != "" {
		// Try deleting from inside the netns first.
		exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "tuntap", "del", "dev", name, "mode", "tap").Run() //nolint:errcheck
		exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "link", "del", name).Run()                         //nolint:errcheck
	}
	// Also try root netns (TAP might not have been moved yet).
	exec.Command(ipBin, "tuntap", "del", "dev", name, "mode", "tap").Run() //nolint:errcheck
	exec.Command(ipBin, "link", "del", name).Run()                         //nolint:errcheck
}

// createTapDevice creates a TAP device using ip tuntap.
// If the device already exists (leftover from a failed run), it is reused.
func createTapDevice(name string) error {
	// Check if TAP already exists in root netns — reuse it.
	if linkExists(name) {
		// Ensure it's up.
		exec.Command(ipBin, "link", "set", name, "up").Run() //nolint:errcheck
		return nil
	}

	cmd := exec.Command(ipBin, "tuntap", "add", "dev", name, "mode", "tap")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip tuntap add %s: %w\n%s", name, err, out)
	}
	cmd = exec.Command(ipBin, "link", "set", name, "up")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set %s up: %w\n%s", name, err, out)
	}
	return nil
}

// linkExists checks if a network link exists in the current netns.
func linkExists(name string) bool {
	return exec.Command(ipBin, "link", "show", name).Run() == nil
}

// linkExistsInNetns checks if a network link exists inside a named netns.
func linkExistsInNetns(name, netnsName string) bool {
	return exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "link", "show", name).Run() == nil
}

// attachTapToBridge moves the TAP into the network's netns and enslaves it
// to the bridge.
func attachTapToBridge(tapName, bridgeName, netnsName string) error {
	// Check if TAP is already in the target netns and enslaved to bridge.
	// Idempotent: skip if already configured.
	nsCheck := exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "link", "show", tapName)
	if nsCheck.Run() == nil {
		// TAP already in netns — check if it's mastered by the bridge.
		brCheck := exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "link", "show", tapName)
		out, _ := brCheck.CombinedOutput()
		if strings.Contains(string(out), "master "+bridgeName) {
			return nil // already configured
		}
		// TAP in netns but not on bridge — enslave it.
		cmd := exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "link", "set", tapName, "master", bridgeName)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("enslave tap to bridge: %w\n%s", err, out)
		}
		return nil
	}

	cmds := [][]string{
		{ipBin, "link", "set", tapName, "netns", netnsName},
		{ipBin, "netns", "exec", netnsName, ipBin, "link", "set", tapName, "up"},
		{ipBin, "netns", "exec", netnsName, ipBin, "link", "set", tapName, "master", bridgeName},
	}
	for _, args := range cmds {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

// deleteTapDevice removes a TAP device.
func deleteTapDevice(name string) error {
	return exec.Command(ipBin, "link", "del", name).Run() //nolint:errcheck
}

// nicIdxFromHandle determines the next NIC index from the typed handle state.
func nicIdxFromHandle(h substrate.NodeHandle) int {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil {
		return 0
	}
	return hs.NICCount
}

// DeleteTapForNIC removes a TAP device (used during destroy).
func DeleteTapForNIC(tapName, netnsName string) error {
	cleanupTap(tapName, netnsName)
	return nil
}
