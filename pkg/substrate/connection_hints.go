package substrate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/secret"
)

// CanonicalConnectionHints decodes persisted desired node settings. Only the
// credential-owning process resolves secrets; plaintext is never re-persisted.
func CanonicalConnectionHints(ctx context.Context, desired any) ([]ConnectionHint, error) {
	if desired == nil {
		return nil, nil
	}
	raw, err := json.Marshal(desired)
	if err != nil {
		return nil, fmt.Errorf("invalid canonical connection configuration")
	}
	var cfg struct {
		Connections []config.ConnectionConfig `json:"connections"`
	}
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("invalid canonical connection configuration")
	}
	resolver := secret.ResolverFromContext(ctx)
	hints := make([]ConnectionHint, len(cfg.Connections))
	for i, c := range cfg.Connections {
		if err := c.ValidateCredentialReferences(); err != nil {
			return nil, err
		}
		password, err := secret.ResolveString(ctx, resolver, c.Password)
		if err != nil {
			return nil, err
		}
		key, err := secret.ResolveString(ctx, resolver, c.PrivateKey)
		if err != nil {
			return nil, err
		}
		hints[i] = ConnectionHint{Type: c.Type, Host: c.Host, User: c.User, Password: password, PrivateKey: key, KnownHosts: c.KnownHosts, InsecureSkipHostKeyCheck: c.InsecureSkipHostKeyCheck}
	}
	return hints, nil
}
