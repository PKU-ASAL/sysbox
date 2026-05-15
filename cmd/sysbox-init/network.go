package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// configureNetworkFromCmdline parses every `ip=` directive present in
// /proc/cmdline and applies it via the `ip(8)` tool. This complements (or
// replaces) the kernel's CONFIG_IP_PNP autoconfigurator, which is absent
// from many distro kernels.
//
// Each directive follows the standard format:
//
//	ip=<client-ip>::<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>
//
// Empty fields are tolerated; we only act when client-ip and device are set.
func configureNetworkFromCmdline() {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		logf("read /proc/cmdline: %v", err)
		return
	}
	for _, tok := range strings.Fields(string(data)) {
		if !strings.HasPrefix(tok, "ip=") {
			continue
		}
		if err := applyIPDirective(strings.TrimPrefix(tok, "ip=")); err != nil {
			logf("apply %q: %v", tok, err)
		}
	}
}

func applyIPDirective(spec string) error {
	parts := strings.Split(spec, ":")
	if len(parts) < 7 {
		return fmt.Errorf("expected 7 fields, got %d", len(parts))
	}
	clientIP := parts[0]
	gw := parts[2]
	mask := parts[3]
	dev := parts[5]
	if clientIP == "" || dev == "" {
		return fmt.Errorf("missing client-ip or device")
	}

	prefix, err := maskToPrefix(mask)
	if err != nil {
		return err
	}

	steps := [][]string{
		{"ip", "link", "set", dev, "up"},
		{"ip", "addr", "add", fmt.Sprintf("%s/%d", clientIP, prefix), "dev", dev},
	}
	if gw != "" {
		steps = append(steps, []string{"ip", "route", "add", "default", "via", gw})
		// Add public DNS so apt-get etc. work when the VM has outbound
		// internet (via router NAT or a direct uplink). If the VM has no
		// external connectivity this is harmless.
		steps = append(steps, []string{"sh", "-c",
			"grep -q 8.8.8.8 /etc/resolv.conf 2>/dev/null || echo 'nameserver 8.8.8.8' >> /etc/resolv.conf"})
	}
	for _, args := range steps {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			// "File exists" / "RTNETLINK answers: File exists" are benign on
			// re-runs (e.g. when kernel IP_PNP already configured the iface).
			if strings.Contains(string(out), "File exists") {
				continue
			}
			return fmt.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	logf("network: configured %s on %s via %s", clientIP, dev, gw)
	return nil
}

// maskToPrefix converts a dotted-quad mask like "255.255.255.0" into a CIDR
// prefix length such as 24.
func maskToPrefix(mask string) (int, error) {
	ip := net.ParseIP(mask)
	if ip == nil {
		return 0, fmt.Errorf("bad mask %q", mask)
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0, fmt.Errorf("only IPv4 mask supported, got %q", mask)
	}
	ones, bits := net.IPv4Mask(v4[0], v4[1], v4[2], v4[3]).Size()
	if bits == 0 {
		return 0, fmt.Errorf("invalid mask %q", mask)
	}
	return ones, nil
}
