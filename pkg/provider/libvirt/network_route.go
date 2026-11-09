package libvirt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) EnsureRoute(ctx context.Context, handle substrate.NodeHandle, destination, via string) error {
	if hsFrom(handle).GuestFamily == substrate.GuestFamilyWindows {
		if err := s.windowsRoute(ctx, handle, destination, via, false); err != nil {
			return err
		}
		return waitWindowsRoute(ctx, 60*time.Second, 3*time.Second, func(probe context.Context) error {
			return s.windowsRoute(probe, handle, destination, via, true)
		})
	}
	connection, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	result, err := connection.Exec(ctx, substrate.ExecRequest{Program: "ip", Args: []string{"route", "replace", destination, "via", via}, Shell: substrate.ShellNone}, io.Discard, io.Discard)
	if err == nil && result.ExitCode != 0 {
		return fmt.Errorf("ip route replace exited %d", result.ExitCode)
	}
	return err
}

func (s *Substrate) HasRoute(ctx context.Context, handle substrate.NodeHandle, destination, via string) (bool, error) {
	if hsFrom(handle).GuestFamily == substrate.GuestFamilyWindows {
		err := s.windowsRoute(ctx, handle, destination, via, true)
		if errors.Is(err, errWindowsRouteMissing) {
			return false, nil
		}
		return err == nil, err
	}
	connection, err := s.Connection(handle, nil)
	if err != nil {
		return false, err
	}
	result, err := connection.Exec(ctx, substrate.ExecRequest{Program: fmt.Sprintf("ip route show %s | grep -F %q", destination, "via "+via), Shell: substrate.ShellLinux}, io.Discard, io.Discard)
	return err == nil && result.ExitCode == 0, err
}

var errWindowsRouteMissing = errors.New("Windows route is missing")

func (s *Substrate) windowsRoute(ctx context.Context, handle substrate.NodeHandle, destination, via string, observeOnly bool) error {
	if _, _, err := net.ParseCIDR(destination); err != nil {
		return fmt.Errorf("libvirt windows: invalid route destination %q", destination)
	}
	viaIP := net.ParseIP(via)
	if viaIP == nil || viaIP.To4() == nil {
		return fmt.Errorf("libvirt windows: invalid route gateway %q", via)
	}
	var mac string
	for _, bridge := range hsFrom(handle).Bridges {
		for _, prefix := range bridge.IPPrefixes {
			_, network, err := net.ParseCIDR(prefix)
			if err == nil && network.Contains(viaIP) {
				mac = strings.ToUpper(strings.ReplaceAll(bridge.MAC, ":", ""))
				break
			}
		}
	}
	if mac == "" {
		return fmt.Errorf("libvirt windows: no NIC shares subnet with gateway %s", via)
	}
	script := windowsRouteScript(mac, destination, via, observeOnly)
	connection, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	result, err := connection.Exec(ctx, substrate.ExecRequest{Program: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command", script}, Shell: substrate.ShellNone}, io.Discard, io.Discard)
	return windowsRouteOutcome(observeOnly, result, err)
}

// PowerShell failures normally exit 1. Reserve 3 for a successful query that
// found no matching route, so observation errors never become absence.
func windowsRouteOutcome(observeOnly bool, result substrate.ExecResult, err error) error {
	if err != nil {
		return err
	}
	if !result.ExitCodeKnown {
		return fmt.Errorf("libvirt windows: route result unknown")
	}
	if result.ExitCode != 0 {
		if observeOnly && result.ExitCode == 3 {
			return errWindowsRouteMissing
		}
		return fmt.Errorf("libvirt windows: route command exited %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}

func windowsRouteScript(mac, destination, via string, observeOnly bool) string {
	missing := "exit 3"
	if !observeOnly {
		missing = fmt.Sprintf("New-NetRoute -InterfaceIndex $nic.ifIndex -DestinationPrefix '%s' -NextHop '%s' | Out-Null", destination, via)
	}
	return fmt.Sprintf(`$ErrorActionPreference='Stop'; $matches=@(Get-NetAdapter -ErrorAction Stop | Where-Object { $_.MacAddress.Replace('-','').ToUpperInvariant() -eq '%s' }); if($matches.Count -ne 1){throw 'route NIC identity missing or ambiguous'}; $nic=$matches[0]; $route=@(Get-NetRoute -AddressFamily IPv4 -ErrorAction Stop | Where-Object { $_.InterfaceIndex -eq $nic.ifIndex -and $_.DestinationPrefix -eq '%s' -and $_.NextHop -eq '%s' }); if($route.Count -eq 0){ %s }`, mac, destination, via, missing)
}
