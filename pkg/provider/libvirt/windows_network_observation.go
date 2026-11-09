package libvirt

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Windows readiness is an observation of the declared interface identity and
// address, not an ICMP policy test. Initialization remains in the existing
// configureWindowsGuestNetwork path; this observer never changes guest state.
func observeWindowsNetwork(ctx context.Context, connection substrate.Connection, bridges []BridgeAttach, timeout, interval time.Duration) (substrate.GuestNetworkInitObservation, error) {
	observation := substrate.GuestNetworkInitObservation{}
	scripts := make([]string, len(bridges))
	for i, bridge := range bridges {
		mac, err := net.ParseMAC(bridge.MAC)
		if err != nil || len(mac) != 6 {
			return observation, fmt.Errorf("libvirt Windows attachment %s: invalid MAC", bridge.Name)
		}
		if len(bridge.IPPrefixes) != 1 {
			return observation, fmt.Errorf("libvirt Windows attachment %s: expected one IPv4 prefix", bridge.Name)
		}
		ip, _, err := net.ParseCIDR(bridge.IPPrefixes[0])
		if err != nil || ip.To4() == nil {
			return observation, fmt.Errorf("libvirt Windows attachment %s: invalid IPv4 prefix", bridge.Name)
		}
		scripts[i] = fmt.Sprintf(`$ErrorActionPreference='Stop'; $a=@(Get-NetAdapter -ErrorAction Stop | Where-Object { $_.MacAddress.Replace('-','').ToUpperInvariant() -eq '%s' }); if($a.Count -gt 1){throw 'ambiguous adapter identity'}; if($a.Count -eq 1){Get-NetIPAddress -AddressFamily IPv4 -ErrorAction Stop | Where-Object { $_.InterfaceIndex -eq $a[0].ifIndex } | ForEach-Object { $_.IPAddress + '/' + $_.PrefixLength }}`, strings.ToUpper(strings.ReplaceAll(mac.String(), ":", "")))
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return observation, err
		}
		if err := bounded.Err(); err != nil {
			return observation, err
		}
		observation.Interfaces = nil
		observation.Converged = true
		observation.Reason = ""
		for i, bridge := range bridges {
			var stdout, stderr strings.Builder
			result, err := connection.Exec(bounded, substrate.ExecRequest{Program: scripts[i], Shell: substrate.ShellPowerShell}, &stdout, &stderr)
			if err != nil {
				observation.Converged = false
				return observation, fmt.Errorf("observe Windows NIC %s: %w", bridge.Name, err)
			}
			if !result.ExitCodeKnown || result.ExitCode != 0 {
				observation.Converged = false
				return observation, fmt.Errorf("observe Windows NIC %s: unsuccessful query", bridge.Name)
			}
			item := substrate.GuestNetworkInterfaceObservation{Name: bridge.Name, MAC: bridge.MAC, IPPrefixes: append([]string(nil), bridge.IPPrefixes...)}
			for _, prefix := range strings.Fields(stdout.String()) {
				if prefix == bridge.IPPrefixes[0] {
					item.Converged = true
				}
			}
			if !item.Converged {
				item.Reason = "declared address not observed on the matching adapter"
				observation.Converged = false
				observation.Reason = item.Reason
			}
			observation.Interfaces = append(observation.Interfaces, item)
		}
		if observation.Converged {
			return observation, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return observation, ctx.Err()
		case <-bounded.Done():
			timer.Stop()
			return observation, nil
		case <-timer.C:
		}
	}
}

// Only an authoritative missing-route observation is retried. The caller has
// already issued EnsureRoute once; transport failures remain unknown and do not
// cause another effect. Every query shares the bounded parent context.
func waitWindowsRoute(ctx context.Context, timeout, interval time.Duration, observe func(context.Context) error) error {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := bounded.Err(); err != nil {
			return err
		}
		err := observe(bounded)
		if err == nil {
			return nil
		}
		if err != errWindowsRouteMissing {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-bounded.Done():
			timer.Stop()
			return errWindowsRouteMissing
		case <-timer.C:
		}
	}
}
