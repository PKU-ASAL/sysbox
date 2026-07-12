package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/value"
)

const desiredHashKey = "desired_hash"
const desiredPayloadKey = "desired"

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
		return "", fmt.Errorf("hash desired %s: %w", n.Address, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func desiredPayload(n *graph.Node) (map[string]any, []string) {
	payload := map[string]any{
		"type": n.Address.Type,
		"name": n.Address.Name,
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
		payload["image"] = cfg.Image
		payload["substrate"] = cfg.Substrate
		payload["vcpus"] = cfg.Vcpus
		payload["memory"] = cfg.Memory
		payload["env"] = cfg.Env
		payload["depends_on"] = cfg.DependsOn
		payload["links"] = normalizeLinks(cfg.Links)
		payload["ports"] = normalizePortConfigs(cfg.Ports)
		payload["routes"] = cfg.Routes
		payload["connections"] = cfg.Connections
		payload["provisioners"] = cfg.Provisioners
		payload["provider_config"] = cfg.ProviderConfig
		if cfg.Lifecycle != nil {
			ignore = cfg.Lifecycle.IgnoreChanges
		}
	case *config.RouterConfig:
		payload["substrate"] = cfg.Substrate
		payload["image"] = cfg.Image
		payload["interfaces"] = cfg.Interfaces
		payload["nat_from"] = cfg.NatFrom
		payload["nat_to"] = cfg.NatTo
		if cfg.Lifecycle != nil {
			ignore = cfg.Lifecycle.IgnoreChanges
		}
	case *config.FirewallConfig:
		payload["attach_to"] = cfg.AttachTo
		payload["rules"] = cfg.Rules
	case *config.SSHAccessConfig:
		payload["node"] = cfg.Node
		payload["authorized_keys"] = cfg.AuthorizedKeys
		payload["bind_ip"] = cfg.BindIP
		payload["port"] = cfg.Port
	case *config.ActorConfig:
		payload["position"] = cfg.Position
		payload["node"] = cfg.Node
		payload["image"] = cfg.Image
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
	payload, _ := desiredPayload(n)
	inst[desiredPayloadKey] = payload
	return nil
}

func stateDesiredHash(r *state.Resource) string {
	if r == nil {
		return ""
	}
	return r.Str(desiredHashKey)
}

func diffDesiredState(n *graph.Node, r *state.Resource) ([]controlplane.FieldChange, string) {
	if n == nil || r == nil {
		return nil, "resource changed"
	}
	after, ignore := desiredPayload(n)
	for _, field := range ignore {
		delete(after, field)
	}
	before, _ := r.AttributeMap()[desiredPayloadKey].(map[string]any)
	if before == nil {
		return nil, "desired configuration hash changed"
	}
	schema := ResourceSchemaFor(n.Address.Type)
	beforeValue, err := dynamicValue(before)
	if err != nil {
		return nil, "prior desired value is invalid"
	}
	afterValue, err := dynamicValue(after)
	if err != nil {
		return nil, "desired value is invalid"
	}
	typedChanges := schema.Diff(beforeValue, afterValue)
	changes := make([]controlplane.FieldChange, len(typedChanges))
	for i, change := range typedChanges {
		changes[i] = controlplane.FieldChange{Path: change.Path.String(), Before: change.Before, After: change.After, RequiresReplace: change.RequiresReplace, Sensitive: change.Sensitive, Computed: change.Computed}
	}
	if len(changes) == 0 {
		return nil, "desired configuration hash changed"
	}
	if anyInPlace(changes) {
		return changes, "desired configuration changed"
	}
	return changes, "desired configuration changed; replacement required"
}

func jsonEqual(a, b any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

func anyInPlace(changes []controlplane.FieldChange) bool {
	for _, ch := range changes {
		if !ch.RequiresReplace {
			return true
		}
	}
	return false
}

func dynamicValue(input any) (value.Value, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return value.Value{}, err
	}
	var result value.Value
	if err := json.Unmarshal(raw, &result); err != nil {
		return value.Value{}, err
	}
	return result, nil
}

func redactIfSensitive(v any, sensitive bool) any {
	if sensitive {
		return "(sensitive)"
	}
	return v
}
