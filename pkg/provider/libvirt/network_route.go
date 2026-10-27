package libvirt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) EnsureRoute(ctx context.Context, handle substrate.NodeHandle, destination, via string) error {
	if hsFrom(handle).GuestFamily == substrate.GuestFamilyWindows {
		return s.windowsRoute(ctx, handle, destination, via, false)
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
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $nic=Get-NetAdapter | Where-Object { $_.MacAddress.Replace('-','').ToUpperInvariant() -eq '%s' } | Select-Object -First 1; if(-not $nic){ throw 'route NIC missing' }; $route=Get-NetRoute -InterfaceIndex $nic.ifIndex -DestinationPrefix '%s' -ErrorAction SilentlyContinue | Where-Object { $_.NextHop -eq '%s' }; if(-not $route){ %s }`, mac, destination, via, func() string {
		if observeOnly {
			return "exit 1"
		}
		return fmt.Sprintf("New-NetRoute -InterfaceIndex $nic.ifIndex -DestinationPrefix '%s' -NextHop '%s' | Out-Null", destination, via)
	}())
	connection, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	result, err := connection.Exec(ctx, substrate.ExecRequest{Program: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command", script}, Shell: substrate.ShellNone}, io.Discard, io.Discard)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		if observeOnly && result.ExitCode == 1 {
			return errWindowsRouteMissing
		}
		return fmt.Errorf("libvirt windows: route command exited %d: %s", result.ExitCode, result.Stderr)
	}
	return nil
}
