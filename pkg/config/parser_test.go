package config

import (
	"os"
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

	require.Len(t, root.Resources, 5)

	require.NotNil(t, findResource(root, "sysbox_network", "dmz"))
	require.NotNil(t, findResource(root, "sysbox_node", "web"))
	require.NotNil(t, findResource(root, "sysbox_actor", "red"))
}

func TestDecodeResource(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "valid_field.hcl")
	root, err := ParseFile(path)
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	netBlock := findResource(root, "sysbox_network", "dmz")
	require.NotNil(t, netBlock)
	var netCfg NetworkConfig
	require.NoError(t, DecodeResource(netBlock, &netCfg, ctx))
	require.Equal(t, "10.0.1.0/24", netCfg.CIDR)

	nodeBlock := findResource(root, "sysbox_node", "web")
	require.NotNil(t, nodeBlock)
	var nodeCfg NodeConfig
	require.NoError(t, DecodeResource(nodeBlock, &nodeCfg, ctx))
	require.Equal(t, "docker", nodeCfg.Substrate)
	require.Equal(t, "sysbox_image.alpine", nodeCfg.Image)
	require.Len(t, nodeCfg.Links, 1)
	require.Equal(t, "dmz", nodeCfg.Links[0].Name)
	require.Equal(t, "10.0.1.10/24", nodeCfg.Links[0].IP)
	require.Equal(t, "sysbox_network.dmz", nodeCfg.Links[0].Network)
}

func TestDecodeNodeLinksRequireLogicalName(t *testing.T) {
	root, err := ParseString(`resource "sysbox_node" "web" {
  image = "alpine"
  substrate = "docker"
  link {
    network = "sysbox_network.public"
    ip = "10.0.0.2/24"
  }
}`, "test.hcl")
	require.NoError(t, err)

	var cfg NodeConfig
	err = DecodeResource(&root.Resources[0], &cfg, nil)
	require.ErrorContains(t, err, "link")
	require.ErrorContains(t, err, "label")
}

func TestDecodeNodeLinksRejectDuplicateLogicalName(t *testing.T) {
	root, err := ParseString(`resource "sysbox_node" "web" {
  image = "alpine"
  substrate = "docker"
  link "uplink" {
    network = "sysbox_network.public"
    ip = "10.0.0.2/24"
  }
  link "uplink" {
    network = "sysbox_network.backup"
    ip = "10.1.0.2/24"
  }
}`, "test.hcl")
	require.NoError(t, err)

	var cfg NodeConfig
	err = DecodeResource(&root.Resources[0], &cfg, nil)
	require.ErrorContains(t, err, `duplicate link name "uplink"`)
}

func TestDecodeNodePorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte(`
substrate "docker" { alias = "local" }
resource "sysbox_node" "web" {
  image = "sysbox_image.nginx.id"
  substrate = substrate.docker.local
  port {
    name = "http"
    target = 80
    published = 28080
    protocol = "http"
    exposure = "host"
    host_ip = "127.0.0.1"
  }
}
`), 0o644))
	root, err := ParseFile(path)
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	nodeBlock := findResource(root, "sysbox_node", "web")
	require.NotNil(t, nodeBlock)
	var nodeCfg NodeConfig
	require.NoError(t, DecodeResource(nodeBlock, &nodeCfg, ctx))
	require.Len(t, nodeCfg.Ports, 1)
	require.Equal(t, "http", nodeCfg.Ports[0].Name)
	require.Equal(t, 80, nodeCfg.Ports[0].Target)
	require.Equal(t, 28080, nodeCfg.Ports[0].Published)
	require.Equal(t, "http", nodeCfg.Ports[0].Protocol)
	require.Equal(t, "host", nodeCfg.Ports[0].Exposure)
	require.Equal(t, "127.0.0.1", nodeCfg.Ports[0].HostIP)
}

func TestDecodeActor(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "valid_field.hcl")
	root, err := ParseFile(path)
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	actorBlock := findResource(root, "sysbox_actor", "red")
	require.NotNil(t, actorBlock)

	var cfg ActorConfig
	require.NoError(t, DecodeResource(actorBlock, &cfg, ctx))
	require.Equal(t, "internal", cfg.Position)
	require.Equal(t, "sysbox_node.client", cfg.Node)
	require.Equal(t, 4096, cfg.Port)
	require.Equal(t, []string{"opencode", "serve", "--port", "4096", "--hostname", "0.0.0.0"}, cfg.Command)
	require.Equal(t, []string{"sysbox_node.client"}, cfg.DependsOn)
}

func TestEvalContextNamespaces(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "testdata", "valid_field.hcl")
	root, err := ParseFile(path)
	require.NoError(t, err)
	ctx, err := BuildEvalContext(root)
	require.NoError(t, err)

	require.Contains(t, ctx.Variables, "substrate")
	require.Contains(t, ctx.Variables, "sysbox_image")
	require.Contains(t, ctx.Variables, "sysbox_network")
	require.Contains(t, ctx.Variables, "sysbox_node")
	require.Contains(t, ctx.Variables, "sysbox_actor")
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
