package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFile(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "valid_field.hcl")

	root, err := ParseFile(path)
	require.NoError(t, err)

	require.Len(t, root.Substrates, 1)
	require.Equal(t, "docker", root.Substrates[0].Type)
	require.Equal(t, "light", root.Substrates[0].Alias)

	require.Len(t, root.Resources, 4)

	network := findResource(root, "sysbox_network", "dmz")
	require.NotNil(t, network)

	web := findResource(root, "sysbox_node", "web")
	require.NotNil(t, web)
}

func TestDecodeResource(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "valid_field.hcl")
	root, err := ParseFile(path)
	require.NoError(t, err)

	netBlock := findResource(root, "sysbox_network", "dmz")
	require.NotNil(t, netBlock)
	var netCfg NetworkConfig
	require.NoError(t, DecodeResource(netBlock, &netCfg))
	require.Equal(t, "10.0.1.0/24", netCfg.CIDR)

	nodeBlock := findResource(root, "sysbox_node", "web")
	require.NotNil(t, nodeBlock)
	var nodeCfg NodeConfig
	require.NoError(t, DecodeResource(nodeBlock, &nodeCfg))
	require.Equal(t, "docker", nodeCfg.Substrate)
	require.Len(t, nodeCfg.Links, 1)
	require.Equal(t, "10.0.1.10/24", nodeCfg.Links[0].IP)
}

func TestParseFileInvalid(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "invalid_field.hcl")
	_, err := ParseFile(path)
	require.Error(t, err)
}

func findResource(root *Root, typ, name string) *ResourceBlock {
	for i := range root.Resources {
		if root.Resources[i].Type == typ && root.Resources[i].Name == name {
			return &root.Resources[i]
		}
	}
	return nil
}
