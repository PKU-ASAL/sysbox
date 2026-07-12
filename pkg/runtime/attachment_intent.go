package runtime

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
)

// AttachmentInput is the resource-independent configuration input for one
// logical network attachment.
type AttachmentInput struct {
	Name       string
	Network    string
	MAC        string
	IPPrefixes []string
	Gateway    string
}

// AttachmentIntent is normalized provider-independent attachment semantics.
type AttachmentIntent struct {
	Name       string
	Network    address.Address
	MAC        string
	IPPrefixes []string
	Gateway    string
}

func DeterministicMAC(topology string, owner address.Address, logicalName string) net.HardwareAddr {
	h := sha256.New()
	for _, part := range []string{topology, owner.String(), logicalName} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(part)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(part))
	}
	mac := net.HardwareAddr(append([]byte(nil), h.Sum(nil)[:6]...))
	mac[0] = (mac[0] | 0x02) & 0xfe
	return mac
}

func NormalizeAttachmentIntents(topology string, owner address.Address, inputs []AttachmentInput) ([]AttachmentIntent, error) {
	intents := make([]AttachmentIntent, 0, len(inputs))
	seenNames := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if input.Name == "" {
			return nil, fmt.Errorf("%s: attachment name is required", owner)
		}
		if _, ok := seenNames[input.Name]; ok {
			return nil, fmt.Errorf("%s: duplicate attachment name %q", owner, input.Name)
		}
		seenNames[input.Name] = struct{}{}

		networkAddress, err := config.ResolveResourceAddress(input.Network, "sysbox_network")
		if err != nil {
			return nil, fmt.Errorf("%s attachment %q: resolve network: %w", owner, input.Name, err)
		}
		mac, err := normalizedAttachmentMAC(input.MAC, topology, owner, input.Name)
		if err != nil {
			return nil, fmt.Errorf("%s attachment %q: %w", owner, input.Name, err)
		}
		prefixes, parsedPrefixes, err := normalizeAttachmentPrefixes(input.IPPrefixes)
		if err != nil {
			return nil, fmt.Errorf("%s attachment %q: %w", owner, input.Name, err)
		}
		gateway, err := normalizeAttachmentGateway(input.Gateway, parsedPrefixes)
		if err != nil {
			return nil, fmt.Errorf("%s attachment %q: %w", owner, input.Name, err)
		}
		intents = append(intents, AttachmentIntent{
			Name: input.Name, Network: networkAddress, MAC: mac,
			IPPrefixes: prefixes, Gateway: gateway,
		})
	}
	return intents, nil
}

func nicSpecsFromAttachmentIntents(intents []AttachmentIntent) []NICSpec {
	specs := make([]NICSpec, 0, len(intents))
	for _, intent := range intents {
		var ip string
		if len(intent.IPPrefixes) > 0 {
			ip = intent.IPPrefixes[0]
		}
		specs = append(specs, NICSpec{
			Name: intent.Name, Network: intent.Network.String(), IP: ip,
			Gateway: intent.Gateway, MAC: intent.MAC,
		})
	}
	return specs
}

func normalizedAttachmentMAC(explicit, topology string, owner address.Address, name string) (string, error) {
	if explicit == "" {
		return DeterministicMAC(topology, owner, name).String(), nil
	}
	mac, err := net.ParseMAC(explicit)
	if err != nil || len(mac) != 6 {
		return "", fmt.Errorf("invalid MAC address %q", explicit)
	}
	if mac[0]&1 != 0 {
		return "", fmt.Errorf("MAC address %q must be unicast", explicit)
	}
	return mac.String(), nil
}

func normalizeAttachmentPrefixes(raw []string) ([]string, []netip.Prefix, error) {
	normalized := make([]string, 0, len(raw))
	parsed := make([]netip.Prefix, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid IP prefix %q: %w", value, err)
		}
		canonical := prefix.String()
		if _, ok := seen[canonical]; ok {
			return nil, nil, fmt.Errorf("duplicate IP prefix %q", canonical)
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
		parsed = append(parsed, prefix)
	}
	return normalized, parsed, nil
}

func normalizeAttachmentGateway(raw string, prefixes []netip.Prefix) (string, error) {
	if raw == "" {
		return "", nil
	}
	gateway, err := netip.ParseAddr(raw)
	if err != nil {
		return "", fmt.Errorf("invalid gateway %q: %w", raw, err)
	}
	for _, prefix := range prefixes {
		if prefix.Contains(gateway) {
			return gateway.String(), nil
		}
	}
	return "", fmt.Errorf("gateway %s is outside every attachment prefix", gateway)
}
