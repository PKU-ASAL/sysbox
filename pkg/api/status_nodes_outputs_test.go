package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/state"
)

// A topology whose HCL declares a sysbox_node and an output block reports both
// under GET /v1/topologies/{name}: nodes is derived from the node resource in
// state, and outputs is the HCL output block evaluated against the eval context.
func TestGetTopologyStatusIncludesNodesAndOutputs(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	s := NewServer(runs, workspaces)

	hclPath := s.workspaceService().HCLFile("lab")
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}

output "web_id" {
  value = sysbox_node.web.id
}
`), 0o644))

	writeState(t, runs, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:      address.Resource("sysbox_node", "web"),
			ResourceType: "sysbox_node",
			Attributes:   map[string]any{"primary_ip": "10.0.0.5"},
			Status:       state.ResourcePresent,
		}},
	})

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.Len(t, body.Status.Nodes, 1)
	require.Equal(t, "web", body.Status.Nodes[0].Name)
	require.Equal(t, "10.0.0.5", body.Status.Nodes[0].Address)
	require.Equal(t, "present", body.Status.Nodes[0].State)
	require.Equal(t, "sysbox_node.web", body.Status.Outputs["web_id"])
}

// An output that references a genuinely unresolvable expression (an undefined
// var) must be skipped, not blank the whole outputs map: the healthy sibling
// output is still reported and the request still succeeds.
func TestGetTopologyStatusSkipsUnresolvableOutputs(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	s := NewServer(runs, workspaces)

	hclPath := s.workspaceService().HCLFile("lab")
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}

output "web_id" {
  value = sysbox_node.web.id
}

output "unresolvable" {
  value = var.not_set
}
`), 0o644))

	writeState(t, runs, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:      address.Resource("sysbox_node", "web"),
			ResourceType: "sysbox_node",
			Attributes:   map[string]any{"primary_ip": "10.0.0.5"},
			Status:       state.ResourcePresent,
		}},
	})

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.Equal(t, "sysbox_node.web", body.Status.Outputs["web_id"])
	_, present := body.Status.Outputs["unresolvable"]
	require.False(t, present, "unresolvable output must be skipped, not returned")
}

// An output that references a real state attribute resolves once the eval
// context is enriched with the resource's state attributes: primary_ip is not
// part of the synthetic {id, name} context, so before enrichment this output
// was skipped.
func TestGetTopologyStatusEnrichesOutputsWithStateAttributes(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	s := NewServer(runs, workspaces)

	hclPath := s.workspaceService().HCLFile("lab")
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}

output "web_ip" {
  value = sysbox_node.web.primary_ip
}
`), 0o644))

	writeState(t, runs, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:      address.Resource("sysbox_node", "web"),
			ResourceType: "sysbox_node",
			Attributes:   map[string]any{"primary_ip": "10.0.0.5"},
			Status:       state.ResourcePresent,
		}},
	})

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.Equal(t, "10.0.0.5", body.Status.Outputs["web_ip"])
}

// Enrichment must not drop the count tuple BuildEvalContext already placed in
// the type namespace: a non-count resource's real attributes are merged in
// alongside the synthetic count instances.
func TestGetTopologyStatusEnrichmentPreservesCountTuples(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	s := NewServer(runs, workspaces)

	hclPath := s.workspaceService().HCLFile("lab")
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}

resource "sysbox_node" "workers" {
  count     = 2
  image     = "alpine"
  substrate = "docker"
}

output "web_ip" {
  value = sysbox_node.web.primary_ip
}

output "worker0_id" {
  value = sysbox_node.workers[0].id
}
`), 0o644))

	writeState(t, runs, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{
			{
				Address:      address.Resource("sysbox_node", "web"),
				ResourceType: "sysbox_node",
				Attributes:   map[string]any{"primary_ip": "10.0.0.5"},
				Status:       state.ResourcePresent,
			},
			{
				Address:      address.IntInstance("sysbox_node", "workers", 0),
				ResourceType: "sysbox_node",
				Attributes:   map[string]any{"primary_ip": "10.0.0.6"},
				Status:       state.ResourcePresent,
			},
			{
				Address:      address.IntInstance("sysbox_node", "workers", 1),
				ResourceType: "sysbox_node",
				Attributes:   map[string]any{"primary_ip": "10.0.0.7"},
				Status:       state.ResourcePresent,
			},
		},
	})

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.Equal(t, "10.0.0.5", body.Status.Outputs["web_ip"])
	require.Equal(t, "sysbox_node.workers[0]", body.Status.Outputs["worker0_id"])
}

// A resource declared in HCL but absent from state keeps its synthetic
// {id, name} binding: enrichment merges, it does not rebuild the namespace.
func TestGetTopologyStatusEnrichmentPreservesDeclaredButAbsentResources(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	s := NewServer(runs, workspaces)

	hclPath := s.workspaceService().HCLFile("lab")
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}

resource "sysbox_node" "db" {
  image     = "postgres"
  substrate = "docker"
}

output "web_ip" {
  value = sysbox_node.web.primary_ip
}

output "db_id" {
  value = sysbox_node.db.id
}
`), 0o644))

	writeState(t, runs, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:      address.Resource("sysbox_node", "web"),
			ResourceType: "sysbox_node",
			Attributes:   map[string]any{"primary_ip": "10.0.0.5"},
			Status:       state.ResourcePresent,
		}},
	})

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.Equal(t, "10.0.0.5", body.Status.Outputs["web_ip"])
	require.Equal(t, "sysbox_node.db", body.Status.Outputs["db_id"])
}

// A module resource (Address.ModulePath set) must not clobber a root resource
// of the same type/name: the root's real attributes win.
func TestGetTopologyStatusModuleResourceDoesNotClobberRoot(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	s := NewServer(runs, workspaces)

	hclPath := s.workspaceService().HCLFile("lab")
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}

output "web_ip" {
  value = sysbox_node.web.primary_ip
}
`), 0o644))

	writeState(t, runs, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{
			{
				Address:      address.Resource("sysbox_node", "web"),
				ResourceType: "sysbox_node",
				Attributes:   map[string]any{"primary_ip": "10.0.0.5"},
				Status:       state.ResourcePresent,
			},
			{
				Address:      address.Resource("sysbox_node", "web").WithModule(address.ModuleInstance{Name: "foo"}),
				ResourceType: "sysbox_node",
				Attributes:   map[string]any{"primary_ip": "10.0.0.99"},
				Status:       state.ResourcePresent,
			},
		},
	})

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.Equal(t, "10.0.0.5", body.Status.Outputs["web_ip"])
}

// A state attribute holding a secret reference must surface as the reference
// string itself, never as materialized plaintext: enrichment converts values
// verbatim and does not resolve secret://input/<name> references.
func TestGetTopologyStatusEnrichmentDoesNotMaterializeSecretPlaintext(t *testing.T) {
	runs := t.TempDir()
	workspaces := t.TempDir()
	s := NewServer(runs, workspaces)

	hclPath := s.workspaceService().HCLFile("lab")
	require.NoError(t, os.MkdirAll(filepath.Dir(hclPath), 0o755))
	require.NoError(t, os.WriteFile(hclPath, []byte(`
resource "sysbox_node" "web" {
  image     = "alpine"
  substrate = "docker"
}

output "web_flag" {
  value = sysbox_node.web.env.FLAG
}
`), 0o644))

	writeState(t, runs, "lab", &state.State{
		Version: state.SchemaVersion,
		Resources: []state.Resource{{
			Address:      address.Resource("sysbox_node", "web"),
			ResourceType: "sysbox_node",
			Attributes: map[string]any{
				"env": map[string]any{"FLAG": "secret://input/flag"},
			},
			Status: state.ResourcePresent,
		}},
	})

	body := getTopology(t, s, "lab")

	require.NotNil(t, body.Status)
	require.Equal(t, "secret://input/flag", body.Status.Outputs["web_flag"])
}
