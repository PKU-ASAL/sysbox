package runtime

import (
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/substrate"
)

func normalizePortConfigs(in []config.PortConfig) []config.PortConfig {
	out := make([]config.PortConfig, 0, len(in))
	for _, p := range in {
		p.Protocol = normalizePortProtocol(p.Protocol)
		p.Exposure = normalizePortExposure(p.Exposure)
		out = append(out, p)
	}
	return out
}

func normalizePortSpecs(in []config.PortConfig) ([]substrate.PortSpec, error) {
	out := make([]substrate.PortSpec, 0, len(in))
	names := map[string]bool{}
	hostBindings := map[string]bool{}
	for _, p := range normalizePortConfigs(in) {
		if p.Target <= 0 || p.Target > 65535 {
			return nil, fmt.Errorf("port %q: target must be between 1 and 65535", p.Name)
		}
		if p.Published < 0 || p.Published > 65535 {
			return nil, fmt.Errorf("port %q: published must be between 1 and 65535", p.Name)
		}
		if !validPortProtocol(p.Protocol) {
			return nil, fmt.Errorf("port %q: protocol must be one of tcp, udp, http, https", p.Name)
		}
		if !validPortExposure(p.Exposure) {
			return nil, fmt.Errorf("port %q: exposure must be one of none, direct, host", p.Name)
		}
		if p.Name != "" {
			if names[p.Name] {
				return nil, fmt.Errorf("port %q: duplicate name", p.Name)
			}
			names[p.Name] = true
		}
		if p.Exposure == substrate.PortExposureHost {
			if p.Published == 0 {
				return nil, fmt.Errorf("port %q: published is required for host exposure", p.Name)
			}
			key := fmt.Sprintf("%s/%d/%s", p.HostIP, p.Published, transportProtocol(p.Protocol))
			if hostBindings[key] {
				return nil, fmt.Errorf("port %q: duplicate host binding %s", p.Name, key)
			}
			hostBindings[key] = true
		}
		out = append(out, substrate.PortSpec{
			Name:      p.Name,
			Target:    p.Target,
			Published: p.Published,
			Protocol:  p.Protocol,
			Exposure:  p.Exposure,
			HostIP:    p.HostIP,
		})
	}
	return out, nil
}

func validatePortExposures(nodeName string, sub substrate.Substrate, ports []substrate.PortSpec) error {
	if len(ports) == 0 {
		return nil
	}
	supported := map[string]bool{}
	for _, exposure := range sub.Capabilities().PortExposures {
		supported[exposure] = true
	}
	for _, p := range ports {
		if !supported[p.Exposure] {
			return fmt.Errorf("node %s port %q: exposure %q is not supported by substrate %s", nodeName, p.Name, p.Exposure, sub.Name())
		}
	}
	return nil
}

func resolvePorts(ports []substrate.PortSpec, primaryIP string) []substrate.ResolvedPort {
	out := make([]substrate.ResolvedPort, 0, len(ports))
	for _, p := range ports {
		r := substrate.ResolvedPort{
			Name:      p.Name,
			Target:    p.Target,
			Published: p.Published,
			Protocol:  p.Protocol,
			Exposure:  p.Exposure,
		}
		switch p.Exposure {
		case substrate.PortExposureHost:
			host := p.HostIP
			if host == "" || host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			r.Host = host
			r.URL = portURL(p.Protocol, host, p.Published)
		case substrate.PortExposureDirect:
			r.Host = primaryIP
			r.TargetHost = primaryIP
			if primaryIP != "" {
				r.URL = portURL(p.Protocol, primaryIP, p.Target)
			}
		}
		out = append(out, r)
	}
	return out
}

func normalizePortProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return "tcp"
	}
	return protocol
}

func normalizePortExposure(exposure string) string {
	exposure = strings.ToLower(strings.TrimSpace(exposure))
	if exposure == "" {
		return substrate.PortExposureDirect
	}
	return exposure
}

func validPortProtocol(protocol string) bool {
	switch normalizePortProtocol(protocol) {
	case "tcp", "udp", "http", "https":
		return true
	default:
		return false
	}
}

func validPortExposure(exposure string) bool {
	switch normalizePortExposure(exposure) {
	case substrate.PortExposureNone, substrate.PortExposureDirect, substrate.PortExposureHost:
		return true
	default:
		return false
	}
}

func transportProtocol(protocol string) string {
	if normalizePortProtocol(protocol) == "udp" {
		return "udp"
	}
	return "tcp"
}

func portURL(protocol, host string, port int) string {
	scheme := normalizePortProtocol(protocol)
	if scheme == "udp" {
		scheme = "udp"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}
