package substrate

import (
	"encoding/json"
	"fmt"
)

// CanonicalGuestUser reads only the account fields of persisted desired node
// configuration. It never supplies a default or exposes connection credentials.
func CanonicalGuestUser(desired any) (string, error) {
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
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("invalid canonical guest configuration")
	}
	user := ""
	if len(cfg.Connections) > 0 {
		user = cfg.Connections[0].User
	}
	if user == "" && cfg.ProviderConfig != nil {
		user = cfg.ProviderConfig.SSHUser
	}
	if user != "" && ValidateSSHAccessUser(user) != nil {
		return "", fmt.Errorf("invalid canonical guest user")
	}
	return user, nil
}
