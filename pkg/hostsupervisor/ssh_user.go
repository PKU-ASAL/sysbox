package hostsupervisor

import (
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// canonicalGuestUser reads the node's persisted desired configuration, never
// a worker payload or a current, potentially unpublished workspace. Decode only
// the account fields; connection credentials must not escape into discovery.
func canonicalGuestUser(resource state.Resource) (string, error) {
	desired := resource.Attributes["desired"]
	if desired == nil {
		return "", nil
	}
	raw, err := json.Marshal(desired)
	if err != nil {
		return "", fmt.Errorf("invalid canonical guest configuration")
	}
	var cfg struct {
		Connections    []struct{ User string }   `json:"connections"`
		ProviderConfig *struct{ SSHUser string } `json:"provider_config"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("invalid canonical guest configuration")
	}
	user := ""
	if len(cfg.Connections) > 0 {
		user = cfg.Connections[0].User
	}
	if user == "" && cfg.ProviderConfig != nil {
		user = cfg.ProviderConfig.SSHUser
	}
	if user != "" && substrate.ValidateSSHAccessUser(user) != nil {
		return "", fmt.Errorf("invalid canonical guest user")
	}
	return user, nil
}

func canonicalConnection(target NodeTarget) (Target, error) {
	if target.canonicalResource == nil {
		return Target{}, fmt.Errorf("canonical guest configuration unavailable")
	}
	user, err := canonicalGuestUser(*target.canonicalResource)
	if err != nil {
		return Target{}, err
	}
	conn, err := target.Node.Connection(target.Handle, nil)
	if err != nil {
		return Target{}, err
	}
	return Target{Scope: target.Scope, AgentID: target.AgentID, NodeID: target.Handle.ID, User: user, Connection: conn}, nil
}
