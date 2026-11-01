package commands

import (
	"fmt"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"os"
	"strings"
)

// This runs before any provider is constructed. The same canonical file and
// minimal environment remain effective for the full lifetime of the service.
func prepareSupervisorProcess() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("host supervisor requires root")
	}
	for _, item := range []struct {
		path    string
		private bool
	}{{supervisorConfigFile, true}, {supervisorIdentityFile, true}} {
		if err := hostsupervisor.ValidateOwnedPath(item.path, item.private); err != nil {
			return err
		}
	}
	cfg, err := config.LoadServiceConfigStrict(supervisorConfigFile)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		path    string
		private bool
	}{{cfg.Agent.ExecutionGrantPublicKeyFile, false}, {cfg.HostSupervisor.SigningKeyFile, true}} {
		if err = hostsupervisor.ValidateOwnedPath(item.path, item.private); err != nil {
			return err
		}
	}
	if cfg.HostSupervisor.WorkerGroupID <= 0 || cfg.HostSupervisor.Socket == "" || cfg.HostSupervisor.Root == "" {
		return fmt.Errorf("host supervisor requires socket, root and non-root worker group")
	}
	env, err := hostsupervisor.HostEnvironment(supervisorConfigFile, cfg)
	if err != nil {
		return err
	}
	// No tool or provider may run until environment replacement is complete.
	os.Clearenv()
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if err = os.Setenv(key, value); err != nil {
			return err
		}
	}
	if err = os.Chdir("/"); err != nil {
		return err
	}
	return hostsupervisor.ValidateHostTools(cfg)
}
