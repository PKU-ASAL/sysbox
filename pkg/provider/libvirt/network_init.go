package libvirt

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

var (
	guestNetworkReadyTimeout = 2 * time.Minute
	guestNetworkPollInterval = time.Second
	guestNetworkProbe        = probeGuestIPv4
)

func (s *Substrate) PrepareGuestNetwork(ctx context.Context, handle substrate.NodeHandle) error {
	hs := hsFrom(handle)
	switch hs.NetworkInit {
	case substrate.GuestNetworkInitCloudInit:
		if err := ensureCloudInitSSHAccess(ctx, hs); err != nil {
			return err
		}
		seedISO, err := createNoCloudSeed(hs.VMDir, hs.DomainName, hs.Bridges, hs.SSHUser, hs.SSHAuthorizedKey)
		if err != nil {
			return err
		}
		hs.SeedISO = seedISO
		return nil
	case substrate.GuestNetworkInitPreconfigured:
		hs.SeedISO = ""
		return nil
	default:
		return fmt.Errorf("libvirt: unsupported guest network initialization mode %q", hs.NetworkInit)
	}
}

func (s *Substrate) ObserveGuestNetwork(ctx context.Context, handle substrate.NodeHandle) (substrate.GuestNetworkInitObservation, error) {
	hs := hsFrom(handle)
	observation := substrate.GuestNetworkInitObservation{Mode: hs.NetworkInit, Converged: len(hs.Bridges) == 0}
	var managedTargets []network.BridgeTarget
	for _, bridge := range hs.Bridges {
		if bridge.Netns == "" {
			if bridge.Target.Managed == nil || bridge.Target.Bridge != bridge.Bridge {
				return observation, fmt.Errorf("libvirt attachment %s: missing namespace or managed root bridge proof", bridge.Name)
			}
			managedTargets = append(managedTargets, bridge.Target)
		}
		for _, prefix := range bridge.IPPrefixes {
			ip, _, err := net.ParseCIDR(prefix)
			if err != nil {
				return observation, fmt.Errorf("libvirt attachment %s: invalid address prefix %q: %w", bridge.Name, prefix, err)
			}
			if ip.To4() == nil {
				return observation, fmt.Errorf("libvirt attachment %s: IPv6 address %q is not supported", bridge.Name, prefix)
			}
		}
	}

	if len(managedTargets) > 0 {
		_, unlock, err := network.LockBridgeTargets(ctx, managedTargets)
		if err != nil {
			return observation, err
		}
		defer unlock()
	}
	deadline := time.Now().Add(guestNetworkReadyTimeout)
	for {
		observation.Interfaces = observation.Interfaces[:0]
		observation.Converged = true
		observation.Reason = ""
		for _, bridge := range hs.Bridges {
			item := substrate.GuestNetworkInterfaceObservation{Name: bridge.Name, MAC: bridge.MAC, IPPrefixes: append([]string(nil), bridge.IPPrefixes...), Converged: true}
			for _, prefix := range bridge.IPPrefixes {
				ip := strings.SplitN(prefix, "/", 2)[0]
				if err := guestNetworkProbe(ctx, bridge.Netns, ip); err != nil {
					item.Converged = false
					item.Reason = fmt.Sprintf("%s from namespace %s: %v", ip, bridge.Netns, err)
					observation.Converged = false
					observation.Reason = item.Reason
					break
				}
			}
			observation.Interfaces = append(observation.Interfaces, item)
		}
		if observation.Converged || time.Now().After(deadline) {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return observation, ctx.Err()
		case <-time.After(guestNetworkPollInterval):
		}
	}
}

func (s *Substrate) configureWindowsGuestNetwork(ctx context.Context, handle substrate.NodeHandle) error {
	hs := hsFrom(handle)
	if hs.GuestFamily != substrate.GuestFamilyWindows || len(hs.Bridges) == 0 {
		return nil
	}
	conn, err := s.Connection(handle, nil)
	if err != nil {
		return err
	}
	waiter, ok := conn.(substrate.ConnectionWaiter)
	if !ok {
		return substrate.ErrNotSupported
	}
	if err := waiter.WaitReady(ctx, 10*time.Minute); err != nil {
		return err
	}
	for _, bridge := range hs.Bridges {
		if len(bridge.IPPrefixes) != 1 || bridge.MAC == "" {
			return fmt.Errorf("libvirt windows: each NIC requires one IPv4 prefix and a MAC")
		}
		ip, network, err := net.ParseCIDR(bridge.IPPrefixes[0])
		if err != nil || ip.To4() == nil {
			return fmt.Errorf("libvirt windows: invalid IPv4 prefix %q", bridge.IPPrefixes[0])
		}
		bits, _ := network.Mask.Size()
		mac, err := net.ParseMAC(bridge.MAC)
		if err != nil {
			return fmt.Errorf("libvirt windows: invalid MAC %q: %w", bridge.MAC, err)
		}
		macText := strings.ToUpper(strings.ReplaceAll(mac.String(), ":", ""))
		gatewayCommand := ""
		if bridge.Gateway != "" {
			gateway := net.ParseIP(bridge.Gateway)
			if gateway == nil || gateway.To4() == nil {
				return fmt.Errorf("libvirt windows: invalid gateway %q", bridge.Gateway)
			}
			gatewayCommand = fmt.Sprintf(`; Get-NetRoute -InterfaceIndex $nic.ifIndex -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Where-Object { $_.NextHop -ne '%s' } | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue; if(-not (Get-NetRoute -InterfaceIndex $nic.ifIndex -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Where-Object { $_.NextHop -eq '%s' })){ try { New-NetRoute -InterfaceIndex $nic.ifIndex -DestinationPrefix '0.0.0.0/0' -NextHop '%s' | Out-Null } catch { if ($_.Exception.Message -notmatch 'already exists') { throw } } }`, gateway, gateway, gateway)
		}
		script := fmt.Sprintf(`$ErrorActionPreference='Stop'; $mac='%s'; $nic=Get-NetAdapter | Where-Object { $_.MacAddress.Replace('-','').ToUpperInvariant() -eq $mac } | Select-Object -First 1; if(-not $nic){ throw "NIC $mac missing" }; Set-NetIPInterface -InterfaceIndex $nic.ifIndex -Dhcp Disabled; Get-NetIPAddress -AddressFamily IPv4 -IPAddress '%s' -ErrorAction SilentlyContinue | Where-Object { $_.InterfaceIndex -ne $nic.ifIndex } | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue; Get-NetIPAddress -InterfaceIndex $nic.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -ne '%s' } | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue; if(-not (Get-NetIPAddress -InterfaceIndex $nic.ifIndex -IPAddress '%s' -ErrorAction SilentlyContinue)){ try { New-NetIPAddress -InterfaceIndex $nic.ifIndex -IPAddress '%s' -PrefixLength %s | Out-Null } catch { if ($_.Exception.Message -notmatch 'already exists') { throw } } }%s; Set-DnsClientServerAddress -InterfaceIndex $nic.ifIndex -ServerAddresses 127.0.0.1; if(-not (Get-NetFirewallRule -DisplayName 'Sysbox ICMPv4' -ErrorAction SilentlyContinue)){ New-NetFirewallRule -DisplayName 'Sysbox ICMPv4' -Direction Inbound -Protocol ICMPv4 -IcmpType 8 -Action Allow | Out-Null }`, macText, ip, ip, ip, ip, strconv.Itoa(bits), gatewayCommand)
		result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command", script}, Shell: substrate.ShellNone}, nil, nil)
		if err != nil {
			return fmt.Errorf("libvirt windows: configure NIC %s: %w", bridge.Name, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("libvirt windows: configure NIC %s exited %d: %s", bridge.Name, result.ExitCode, result.Stderr)
		}
	}
	return nil
}

func probeGuestIPv4(ctx context.Context, namespace, ip string) error {
	program, args := "ping", []string{"-c", "1", "-W", "1", ip}
	if namespace != "" {
		program = "ip"
		args = append([]string{"netns", "exec", namespace, "ping"}, args...)
	}
	output, err := exec.CommandContext(ctx, program, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ping: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
