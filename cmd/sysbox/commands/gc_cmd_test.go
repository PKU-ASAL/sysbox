package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func TestGCIncludesCanonicalStateAndRefusesUnresolvedActions(t *testing.T) {
	root := t.TempDir()
	repo, err := persistence.OpenFilesystem(filepath.Join(root, "lab", "state.json.lifecycle"))
	require.NoError(t, err)
	defer repo.Close()
	ctx := context.Background()
	st, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "live-container", OwnerUID: st.TopologyUID, ResourceUID: st.TopologyUID + "/web", Generation: 1})
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	require.True(t, active.containers["live-container"], "GC must read the same state as apply")
	latest, err := repo.LoadState(ctx, "lab")
	require.NoError(t, err)
	require.NoError(t, repo.CommitActionIntent(ctx, runtime.ActionRecord{ID: "unknown-create", RunID: "run", Topology: "lab", Resource: address.Resource("sysbox_node", "other"), Action: controlplane.PlanActionCreate, Status: runtime.ActionIntentCommitted, OwnerUID: st.TopologyUID, ResourceUID: st.TopologyUID + "/other", Generation: 1, StateSerial: latest.Meta.Serial, ExpectedSerial: latest.Meta.Serial}))
	_, err = collectActiveIDsChecked(root)
	require.Error(t, err, "unconfirmed resources must not be reclaimed")
}

func TestGCExecutePersistsUncertainDeleteBeforeReturning(t *testing.T) {
	root, owner := gcInventoryFixture(t)
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	candidate := orphan{Kind: "unsupported", ID: "external", Topo: owner, OwnerUID: owner, ResourceUID: owner + "/orphan", Generation: 1, InventoryDigest: active.inventories[owner]}
	err = executeOrphans(context.Background(), root, []orphan{candidate}, io.Discard)
	require.ErrorContains(t, err, "unsupported GC provider kind")
	journal, err := loadGCJournal(filepath.Join(root, "_gc", "deletion-journal.json"))
	require.NoError(t, err)
	record, ok := journal.Records["unsupported:external"]
	require.True(t, ok)
	require.Equal(t, "uncertain", record.Status)
	require.NotEmpty(t, record.Digest)
}

func TestGCExecuteDockerDeletesAndRecordsCompletion(t *testing.T) {
	root, owner := gcInventoryFixture(t)
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/version":
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41","Version":"test"}`))
		case strings.Contains(r.URL.Path, "/containers/orphan") && r.Method == http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/containers/orphan") && r.Method == http.MethodGet:
			if deleted {
				w.WriteHeader(http.StatusNotFound)
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "orphan", "Config": map[string]any{"Labels": map[string]string{
					runtime.LabelManaged: "true", "sysbox.owner_uid": owner, "sysbox.topology_uid": owner, "sysbox.resource_uid": owner + "/orphan", "sysbox.generation": "1",
				}}})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", server.URL)
	candidate := orphan{Kind: "container", ID: "orphan", Topo: owner, OwnerUID: owner, ResourceUID: owner + "/orphan", Generation: 1, InventoryDigest: active.inventories[owner]}
	require.NoError(t, executeOrphans(context.Background(), root, []orphan{candidate}, io.Discard))
	require.True(t, deleted)
	journal, err := loadGCJournal(filepath.Join(root, "_gc", "deletion-journal.json"))
	require.NoError(t, err)
	require.Equal(t, "complete", journal.Records["container:orphan"].Status)
}

func TestGCProtectsResourcesReferencedOnlyBySnapshot(t *testing.T) {
	root := t.TempDir()
	repo, err := persistence.OpenFilesystem(filepath.Join(root, "lab", "state.json.lifecycle"))
	require.NoError(t, err)
	defer repo.Close()
	manager := state.NewManagerWithBackend(repo.StateBackend("lab"))
	st, err := manager.Load()
	require.NoError(t, err)
	st.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "snapshot-container", OwnerUID: st.TopologyUID, ResourceUID: "node", Generation: 1}}
	require.NoError(t, manager.Save(st))
	snapshot, err := manager.Snapshot(context.Background(), "retain resource reference")
	require.NoError(t, err)
	st.Resources = nil
	require.NoError(t, manager.Save(st))
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	require.True(t, active.containers["snapshot-container"], "a retained snapshot is a GC reference even after removal from current state")
	require.NoError(t, manager.Backend().(state.SnapshotDeleter).DeleteSnapshot(context.Background(), snapshot.ID))
	active, err = collectActiveIDsChecked(root)
	require.NoError(t, err)
	require.False(t, active.containers["snapshot-container"])
}

func gcInventoryFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	repo, err := persistence.OpenFilesystem(filepath.Join(root, "lab", "state.json.lifecycle"))
	require.NoError(t, err)
	defer repo.Close()
	st, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	st.AddResource(state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "active-container", OwnerUID: st.TopologyUID, ResourceUID: st.TopologyUID + "/web", Generation: 1})
	require.NoError(t, state.NewManagerWithBackend(repo.StateBackend("lab")).Save(st))
	return root, st.TopologyUID
}
func gcFakeVirsh(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\n"+body), 0o755))
	t.Setenv("PATH", bin)
}
func gcFakeDocker(t *testing.T, containers []container.Summary, networks []network.Summary) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Errorf("GC attempted mutation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode(containers)
		case strings.HasSuffix(r.URL.Path, "/networks"):
			_ = json.NewEncoder(w).Encode(networks)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", server.URL)
}
func gcFlags(t *testing.T, runs string, dry bool, backend string) {
	t.Helper()
	oldRuns, oldDry, oldBackend, oldState := flagGcRunsDir, flagGcDryRun, flagBackend, flagStateFile
	t.Cleanup(func() {
		flagGcRunsDir, flagGcDryRun, flagBackend, flagStateFile = oldRuns, oldDry, oldBackend, oldState
	})
	flagGcRunsDir, flagGcDryRun, flagBackend, flagStateFile = runs, dry, backend, "local-state.json"
}
func gcLabels(owner string) map[string]string {
	return map[string]string{runtime.LabelManaged: "true", "sysbox.owner_uid": owner, "sysbox.topology_uid": owner, "sysbox.resource_uid": owner + "/resource", "sysbox.generation": "1"}
}

func TestGCSkipsUninventoriedDockerOwnersAndLegacyLabels(t *testing.T) {
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_WORKDIR", t.TempDir())
	root, owner := gcInventoryFixture(t)
	gcFakeVirsh(t, "exit 0\n")
	gcFakeDocker(t, []container.Summary{{ID: "owned-candidate", Labels: gcLabels(owner)}, {ID: "foreign-deployment", Labels: gcLabels("another-owner")}, {ID: "legacy", Labels: map[string]string{runtime.LabelManaged: "true"}}}, []network.Summary{{ID: "foreign-network", Labels: gcLabels("another-owner")}})
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	found, err := findOrphans(context.Background(), active)
	require.NoError(t, err)
	var ids []string
	for _, candidate := range found {
		ids = append(ids, candidate.ID)
	}
	require.Equal(t, []string{"owned-candidate"}, ids)
	gcFlags(t, root, true, "")
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	require.NoError(t, runGc(cmd, nil))
	require.Contains(t, output.String(), "skipped")
	require.Contains(t, output.String(), "foreign-deployment")
}

func TestGCAutomaticDeletionIsDisabledWithoutFencedInventory(t *testing.T) {
	gcFakeVirsh(t, "exit 0\n")
	gcFakeDocker(t, []container.Summary{}, []network.Summary{})
	gcFlags(t, t.TempDir(), false, "")
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	require.ErrorContains(t, runGc(cmd, nil), "automatic GC deletion is disabled")
	require.ErrorContains(t, removeOrphans(context.Background(), nil), "automatic GC deletion is disabled")
}

func TestGCRejectsBackendNotCoveredByFilesystemInventory(t *testing.T) {
	gcFakeVirsh(t, "exit 0\n")
	gcFakeDocker(t, []container.Summary{}, []network.Summary{})
	gcFlags(t, t.TempDir(), true, "sqlite:///not-inventoried.sqlite")
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	require.ErrorContains(t, runGc(cmd, nil), "backend")
}

func TestCollectActiveIDsRejectsStandaloneState(t *testing.T) {
	runsDir := t.TempDir()

	st := &state.State{Resources: []state.Resource{
		{Address: address.Resource("sysbox_node", "web"), Driver: "docker", ExternalID: "container-1"},
		{Address: address.Resource("sysbox_network", "lab"), Driver: "docker", ExternalID: "network-1"},
		{Address: address.Resource("sysbox_node", "vm"), Driver: "libvirt", ExternalID: "domain-1"},
	}}
	require.NoError(t, state.NewManager(filepath.Join(runsDir, "lab", "state.json")).Save(st))

	_, err := collectActiveIDsChecked(runsDir)
	require.ErrorContains(t, err, "explicit rebuild required")
}

func TestCollectActiveIDsCheckedFailsClosedOnCorruptState(t *testing.T) {
	runsDir := t.TempDir()
	path := filepath.Join(runsDir, "lab", "state.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{corrupt"), 0o600))
	_, err := collectActiveIDsChecked(runsDir)
	require.Error(t, err)
}

func TestFindOrphansDocker(t *testing.T) {
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_WORKDIR", t.TempDir())
	gcFakeVirsh(t, "exit 0\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]container.Summary{
				{ID: "orphan-container", Labels: gcLabels("owner")},
				{ID: "active-container", Labels: gcLabels("owner")},
				{ID: "unmanaged", Labels: map[string]string{}}, // no sysbox.managed, must be ignored
			})
		case strings.Contains(r.URL.Path, "/networks"):
			_ = json.NewEncoder(w).Encode([]network.Summary{
				{ID: "orphan-network", Labels: gcLabels("owner")},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", server.URL)

	active := activeIDs{
		owners:     map[string]bool{"owner": true},
		containers: map[string]bool{"active-container": true},
		networks:   map[string]bool{},
		domains:    map[string]bool{},
	}

	orphans, err := findOrphans(context.Background(), active)
	require.NoError(t, err)

	var kinds []string
	for _, o := range orphans {
		kinds = append(kinds, o.Kind+":"+o.ID)
	}
	require.Contains(t, kinds, "container:orphan-container")
	require.Contains(t, kinds, "network:orphan-network")
	require.NotContains(t, kinds, "container:active-container")
	require.NotContains(t, kinds, "container:unmanaged")
}

func TestGCLibvirtRequiresInventoriedOwnerMetadataAndUsesUUID(t *testing.T) {
	root, owner := gcInventoryFixture(t)
	active, err := collectActiveIDsChecked(root)
	require.NoError(t, err)
	const live = "11111111-2222-4333-8444-555555555551"
	const candidate = "11111111-2222-4333-8444-555555555552"
	const foreign = "11111111-2222-4333-8444-555555555553"
	const legacy = "11111111-2222-4333-8444-555555555554"
	active.domains[live] = true
	t.Setenv("SYSBOX_GC_TEST_OWNER", owner)
	gcFakeVirsh(t, `case "$1" in
list) printf "%s\n" 11111111-2222-4333-8444-555555555551 11111111-2222-4333-8444-555555555552 11111111-2222-4333-8444-555555555553 11111111-2222-4333-8444-555555555554;;
dumpxml)
 case "$2" in
  *553) owner=foreign-owner;;
  *554) printf '<domain><uuid>%s</uuid><title>sysbox-managed</title></domain>\n' "$2";exit 0;;
  *) owner="$SYSBOX_GC_TEST_OWNER";;
 esac
 printf '<domain><name>same-name-is-not-identity</name><uuid>%s</uuid><metadata><managed xmlns="urn:sysbox:managed" owner_uid="%s" topology_uid="%s" resource_uid="%s/resource" generation="1"/></metadata></domain>\n' "$2" "$owner" "$owner" "$owner";;
*) exit 99;;
esac
`)
	var report bytes.Buffer
	found, err := findOrphanDomains(context.Background(), active, &report)
	require.NoError(t, err)
	require.Equal(t, []orphan{{Kind: "domain", ID: candidate, Topo: owner, OwnerUID: owner, ResourceUID: owner + "/resource", Generation: 1, InventoryDigest: active.inventories[owner]}}, found)
	require.Contains(t, report.String(), "skipped domain "+foreign)
	require.Contains(t, report.String(), "skipped domain "+legacy)
}

func TestGCEmptyCanonicalDirectoryCannotMintOwnerInventory(t *testing.T) {
	root := t.TempDir()
	aggregate := filepath.Join(root, "lab", "state.json.lifecycle")
	require.NoError(t, os.MkdirAll(aggregate, 0o700))
	_, err := collectActiveIDsChecked(root)
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(aggregate, "lab.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestGCLegacyInventoryDoesNotAuthorizeHostWideScan(t *testing.T) {
	var report bytes.Buffer
	found, err := findOrphansWithReport(context.Background(), activeIDs{containers: map[string]bool{"legacy-container": true}}, &report)
	require.NoError(t, err)
	require.Empty(t, found)
	require.Contains(t, report.String(), "skipped host scan")
}

func TestGCPreviewDeclaresExternalInventoryExcluded(t *testing.T) {
	gcFlags(t, t.TempDir(), true, "")
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&output)
	require.NoError(t, runGc(cmd, nil))
	require.Contains(t, output.String(), "SQL/remote backends and other runs directories are not inventoried")
}
