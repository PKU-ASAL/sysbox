package config

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/secret"
)

// ValidateCredentialReferences prevents explicit WinRM configuration from
// persisting a cleartext password in the desired resource or revision.
func (c ConnectionConfig) ValidateCredentialReferences() error {
	if c.Type != "winrm" {
		return nil
	}
	if c.InsecureSkipHostKeyCheck || c.KnownHosts != "" || c.PrivateKey != "" {
		return fmt.Errorf("WinRM requires verified HTTPS and does not accept SSH trust overrides")
	}
	if c.User == "" || c.Host == "" {
		return fmt.Errorf("WinRM requires explicit guest endpoint and user")
	}
	ref, err := secret.Parse(c.Password)
	if err != nil || (ref.Source != "env" && ref.Source != "input") {
		return fmt.Errorf("WinRM password must be an input or environment secret reference")
	}
	return nil
}
