package runtime

import (
	"testing"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/stretchr/testify/require"
)

func TestNodeProvisionersOptionalSysArmorBeforeScenario(t *testing.T) {
	challenge := []config.ProvisionerConfig{{Type: "exec", Program: "/bin/true", Shell: "none"}}
	require.Equal(t, challenge, nodeProvisioners(&firecracker.Config{}, challenge))
	enabled := nodeProvisioners(&firecracker.Config{SysArmor: true}, challenge)
	require.Len(t, enabled, 5)
	require.Equal(t, "file", enabled[0].Type)
	require.Equal(t, "/tmp/sysarmor-agent-deploy.tar.gz", enabled[0].Destination)
	require.Equal(t, "/bin/bash", enabled[3].Program)
	require.Equal(t, challenge[0], enabled[4])
	require.Len(t, challenge, 1)
	require.Equal(t, challenge, nodeProvisioners(struct{}{}, challenge))
}
