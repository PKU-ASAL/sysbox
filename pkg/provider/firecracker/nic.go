package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC creates a TAP device, connects it to the network's bridge,
// and records the NIC in the VM config JSON. The NIC will be available
// when StartNode launches the Firecracker process.
//
// Firecracker does NOT support NIC hot-plug in config-file mode,
// so we must declare all interfaces before boot.
type attachmentState struct {
	Tap         string `json:"tap"`
	NetNS       string `json:"netns"`
	GuestDevice string `json:"guest_device"`
}
type networkState struct {
	NetNS  string `json:"netns"`
	Bridge string `json:"bridge"`
}
type linkRequest struct{ Name, NetNS, Bridge, IP, Gateway, MAC string }
type attachedNIC struct{ Kind, HostEnd, GuestEnd, IP, NetNS string }

func attachmentTapName(nodeID, logicalName string) string {
	value := strings.TrimPrefix(nodeID, "sysbox-") + "-" + logicalName
	if len(value) <= 11 {
		return "tap-" + value
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return fmt.Sprintf("tap-%08x", h.Sum32())
}

func (s *Substrate) Attach(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	hs, ok := h.Provider.(*HandleState)
	if !ok || hs == nil || hs.ConfigPath == "" {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "VM config path missing", nil)
	}
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode network state", err)
	}
	ip := ""
	if len(req.IPPrefixes) > 0 {
		ip = req.IPPrefixes[0]
	}
	attached, err := s.attachNIC(ctx, h, linkRequest{Name: req.Name, NetNS: target.NetNS, Bridge: target.Bridge, IP: ip, Gateway: req.Gateway, MAC: req.MAC})
	if err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnavailable, "firecracker", "attach network", err)
	}
	guest := fmt.Sprintf("eth%d", hs.NICCount-1)
	raw, _ := json.Marshal(attachmentState{Tap: attached.HostEnd, NetNS: attached.NetNS, GuestDevice: guest})
	return driver.AttachmentResult{Driver: "firecracker", GuestDevice: guest, State: raw}, nil
}
func (s *Substrate) Observe(_ context.Context, _ substrate.NodeHandle, _ driver.AttachmentRequest, raw json.RawMessage) (driver.AttachmentResult, error) {
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode attachment state", err)
	}
	if !linkExists(st.Tap) && !linkExistsInNetns(st.Tap, st.NetNS) {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, "firecracker", "tap not found", nil)
	}
	return driver.AttachmentResult{Driver: "firecracker", GuestDevice: st.GuestDevice, State: raw}, nil
}
func (s *Substrate) Delete(ctx context.Context, _ substrate.NodeHandle, _ driver.AttachmentRequest, raw json.RawMessage) error {
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode attachment state", err)
	}
	if !linkExists(st.Tap) && !linkExistsInNetns(st.Tap, st.NetNS) {
		return nil
	}
	if st.NetNS != "" {
		if err := exec.CommandContext(ctx, ipBin, "netns", "exec", st.NetNS, ipBin, "link", "del", st.Tap).Run(); err != nil {
			return driver.Wrap(driver.ErrorUnavailable, "firecracker", "delete tap", err)
		}
		return nil
	}
	if err := deleteTapDevice(st.Tap); err != nil {
		return driver.Wrap(driver.ErrorUnavailable, "firecracker", "delete tap", err)
	}
	return nil
}

func (s *Substrate) attachNIC(ctx context.Context, h substrate.NodeHandle, req linkRequest) (attachedNIC, error) {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil || hs.ConfigPath == "" {
		return attachedNIC{}, fmt.Errorf("VM config path not found in handle provider state")
	}

	tapName := attachmentTapName(h.ID, req.Name)

	netnsName := req.NetNS
	bridgeName := req.Bridge

	// Create or reuse the TAP device.
	tapInRoot := linkExists(tapName)
	tapInNetns := netnsName != "" && linkExistsInNetns(tapName, netnsName)

	if !tapInRoot && !tapInNetns {
		if err := createTapDevice(tapName); err != nil {
			return attachedNIC{}, fmt.Errorf("create tap %s: %w", tapName, err)
		}
	} else {
		if tapInRoot {
			if err := exec.CommandContext(ctx, ipBin, "link", "set", tapName, "up").Run(); err != nil {
				slog.Warn("set tap up in root netns", "tap", tapName, "error", err)
			}
		} else if tapInNetns {
			if err := exec.CommandContext(ctx, ipBin, "netns", "exec", netnsName, ipBin, "link", "set", tapName, "up").Run(); err != nil {
				slog.Warn("set tap up in netns", "tap", tapName, "netns", netnsName, "error", err)
			}
		}
	}

	// Attach TAP to the network bridge.
	if netnsName != "" && bridgeName != "" {
		if err := attachTapToBridge(ctx, tapName, bridgeName, netnsName); err != nil {
			return attachedNIC{}, fmt.Errorf("attach tap to bridge: %w", err)
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
		return attachedNIC{}, fmt.Errorf("append NIC to config: %w", err)
	}

	// Phase A: kernel cmdline IP autoconfig for the first interface.
	if nicIdx == 0 && req.IP != "" {
		hostname := strings.TrimPrefix(h.ID, "sysbox-")
		if err := injectKernelIPArg(cfgPath, ifaceID, hostname, req.IP, req.Gateway); err != nil {
			return attachedNIC{}, fmt.Errorf("inject kernel ip= arg: %w", err)
		}
	}

	hs.NICCount = nicIdx + 1
	hs.TapName = tapName

	return attachedNIC{
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

	replaced := false
	for i := range cfg.NetworkInterfaces {
		if cfg.NetworkInterfaces[i].IfaceID == iface.IfaceID {
			cfg.NetworkInterfaces[i] = iface
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.NetworkInterfaces = append(cfg.NetworkInterfaces, iface)
	}

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
		if err := exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "tuntap", "del", "dev", name, "mode", "tap").Run(); err != nil {
			slog.Debug("cleanup tap tuntap in netns", "tap", name, "error", err)
		}
		if err := exec.Command(ipBin, "netns", "exec", netnsName, ipBin, "link", "del", name).Run(); err != nil {
			slog.Debug("cleanup tap link in netns", "tap", name, "error", err)
		}
	}
	// Also try root netns (TAP might not have been moved yet).
	if err := exec.Command(ipBin, "tuntap", "del", "dev", name, "mode", "tap").Run(); err != nil {
		slog.Debug("cleanup tap tuntap in root", "tap", name, "error", err)
	}
	if err := exec.Command(ipBin, "link", "del", name).Run(); err != nil {
		slog.Debug("cleanup tap link in root", "tap", name, "error", err)
	}
}

// createTapDevice creates a TAP device using ip tuntap.
// If the device already exists (leftover from a failed run), it is reused.
func createTapDevice(name string) error {
	// Check if TAP already exists in root netns — reuse it.
	if linkExists(name) {
		if err := exec.Command(ipBin, "link", "set", name, "up").Run(); err != nil {
			slog.Warn("set existing tap up", "tap", name, "error", err)
		}
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
func attachTapToBridge(ctx context.Context, tapName, bridgeName, netnsName string) error {
	// Check if TAP is already in the target netns and enslaved to bridge.
	// Idempotent: skip if already configured.
	nsCheck := exec.CommandContext(ctx, ipBin, "netns", "exec", netnsName, ipBin, "link", "show", tapName)
	if nsCheck.Run() == nil {
		// TAP already in netns — check if it's mastered by the bridge.
		brCheck := exec.CommandContext(ctx, ipBin, "netns", "exec", netnsName, ipBin, "link", "show", tapName)
		out, _ := brCheck.CombinedOutput()
		if strings.Contains(string(out), "master "+bridgeName) {
			return nil // already configured
		}
		// TAP in netns but not on bridge — enslave it.
		cmd := exec.CommandContext(ctx, ipBin, "netns", "exec", netnsName, ipBin, "link", "set", tapName, "master", bridgeName)
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
		if out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

// deleteTapDevice removes a TAP device.
func deleteTapDevice(name string) error {
	return exec.Command(ipBin, "link", "del", name).Run()
}

// DeleteTapForNIC removes a TAP device (used during destroy).
func DeleteTapForNIC(tapName, netnsName string) error {
	cleanupTap(tapName, netnsName)
	return nil
}
