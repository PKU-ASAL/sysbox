package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
)

const desiredHashKey = "desired_hash"

// desiredHash returns a stable hash of the user-facing desired configuration
// for a graph node. It deliberately excludes realized provider state such as
// container IDs, vm directories, and assigned runtime endpoints.
func desiredHash(n *graph.Node) (string, error) {
	if n == nil {
		return "", fmt.Errorf("nil graph node")
	}
	payload, ignore := desiredPayload(n)
	if len(ignore) > 0 {
		for _, field := range ignore {
			delete(payload, field)
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("hash desired %s: %w", n.ID, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func desiredPayload(n *graph.Node) (map[string]any, []string) {
	payload := map[string]any{
		"type": n.ID.Type,
		"name": n.ID.Name,
	}
	var ignore []string

	switch cfg := n.Data.(type) {
	case *config.NetworkConfig:
		payload["cidr"] = cfg.CIDR
		payload["network_type"] = cfg.Type
		payload["nat"] = cfg.NAT
		if cfg.Lifecycle != nil {
			ignore = cfg.Lifecycle.IgnoreChanges
		}
	case *config.ImageConfig:
		payload["substrate"] = cfg.Substrate
		payload["docker_ref"] = cfg.DockerRef
		payload["rootfs"] = cfg.Rootfs
		payload["qcow2"] = cfg.QCow2
		payload["sha256"] = cfg.SHA256
		payload["size"] = cfg.Size
	case *config.KernelConfig:
		payload["substrate"] = cfg.Substrate
		payload["source"] = cfg.Source
		payload["sha256"] = cfg.SHA256
		payload["cmdline_template"] = cfg.CmdlineTemplate
		payload["depends_on"] = cfg.DependsOn
	case *config.NodeConfig:
		payload["image"] = config.ResolveName(cfg.Image)
		payload["substrate"] = cfg.Substrate
		payload["vcpus"] = cfg.Vcpus
		payload["memory"] = cfg.Memory
		payload["env"] = cfg.Env
		payload["depends_on"] = cfg.DependsOn
		payload["links"] = normalizeLinks(cfg.Links)
		payload["routes"] = cfg.Routes
		payload["connections"] = cfg.Connections
		payload["provisioners"] = cfg.Provisioners
		payload["provider_config"] = cfg.ProviderConfig
		if cfg.Lifecycle != nil {
			ignore = cfg.Lifecycle.IgnoreChanges
		}
	case *config.RouterConfig:
		payload["substrate"] = cfg.Substrate
		payload["image"] = config.ResolveName(cfg.Image)
		payload["interfaces"] = cfg.Interfaces
		payload["nat_from"] = cfg.NatFrom
		payload["nat_to"] = cfg.NatTo
		if cfg.Lifecycle != nil {
			ignore = cfg.Lifecycle.IgnoreChanges
		}
	case *config.FirewallConfig:
		payload["attach_to"] = config.ResolveName(cfg.AttachTo)
		payload["rules"] = cfg.Rules
	case *config.SSHAccessConfig:
		payload["node"] = config.ResolveName(cfg.Node)
		payload["authorized_keys"] = cfg.AuthorizedKeys
		payload["bind_ip"] = cfg.BindIP
		payload["port"] = cfg.Port
	case *config.ActorConfig:
		payload["position"] = cfg.Position
		payload["node"] = config.ResolveName(cfg.Node)
		payload["image"] = config.ResolveName(cfg.Image)
		payload["links"] = normalizeLinks(cfg.Links)
		payload["command"] = cfg.Command
		payload["port"] = cfg.Port
		payload["acp_ip"] = cfg.ACPIP
		payload["env"] = cfg.Env
		payload["entry_points"] = cfg.EntryPoints
		payload["depends_on"] = cfg.DependsOn
	default:
		payload["data"] = cfg
	}
	return payload, ignore
}

func normalizeLinks(in []config.LinkConfig) []config.LinkConfig {
	out := make([]config.LinkConfig, 0, len(in))
	for _, link := range in {
		link.Network = config.ResolveName(link.Network)
		out = append(out, link)
	}
	return out
}

func setDesiredHash(n *graph.Node, inst map[string]any) error {
	if inst == nil {
		return nil
	}
	h, err := desiredHash(n)
	if err != nil {
		return err
	}
	inst[desiredHashKey] = h
	return nil
}

func stateDesiredHash(r *state.Resource) string {
	if r == nil {
		return ""
	}
	return r.Str(desiredHashKey)
}
