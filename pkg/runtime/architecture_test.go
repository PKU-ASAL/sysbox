package runtime

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

// The resource engine must compile without control-plane request/lease types.
// Checking imports rather than names also catches renamed import aliases.
func TestResourceEngineDoesNotImportControlPlane(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			require.NoError(t, err)
			require.NotEqual(t, "github.com/oslab/sysbox/pkg/controlplane", path, "%s depends on dispatch contracts", entry.Name())
		}
	}
}

func TestResourceEngineDoesNotSelectGlobalProviders(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(entry.Name())
		require.NoError(t, err)
		require.NotContains(t, string(raw), "driver.DefaultRegistry", "%s must use the caller's provider registry", entry.Name())
	}
}
