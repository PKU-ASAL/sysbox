package runtime

import "github.com/oslab/sysbox/pkg/config"

// nodeProvisioners prepends optional guest setup before the scenario's
// provisioners, both on first create and after reset.
func nodeProvisioners(providerConfig any, declared []config.ProvisionerConfig) []config.ProvisionerConfig {
	bootstrap, ok := providerConfig.(interface {
		BootstrapProvisioners() []config.ProvisionerConfig
	})
	if !ok {
		return declared
	}
	initial := bootstrap.BootstrapProvisioners()
	if len(initial) == 0 {
		return declared
	}
	result := make([]config.ProvisionerConfig, 0, len(initial)+len(declared))
	result = append(result, initial...)
	return append(result, declared...)
}
