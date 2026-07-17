package runtime

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoreDoesNotDependOnConcreteInfrastructure(t *testing.T) {
	for _, root := range []string{".", "../api", "../agentexec"} {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{
				"pkg/provider/", "substrate.Get(", "exec.Command(", "exec.CommandContext(", "iptablesCmd",
				`"nsenter"`, `"nft"`, `"ssh"`, `"winrm"`,
			} {
				require.NotContains(t, string(data), forbidden, "%s crosses the driver boundary", path)
			}
			return nil
		}))
	}
}

func TestCoreDoesNotExposeActorOrACPResources(t *testing.T) {
	for _, root := range []string{".", "../config", "../api", "../controlplane"} {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{"ActorConfig", "sysbox_actor", "entry_points", "acp_url"} {
				require.NotContains(t, string(data), forbidden, "%s retains actor/ACP resource semantics", path)
			}
			return nil
		}))
	}
}
