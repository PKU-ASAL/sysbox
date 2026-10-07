package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

var (
	flagGcRunsDir string
	flagGcDryRun  bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Preview unreferenced resources owned by inventoried local topologies",
	Long: `Preview Docker and libvirt resources whose durable ownership matches a
canonical topology inventoried under --runs. Uninventoried owners and resources
without durable ownership metadata are skipped explicitly. Configured backends
outside this filesystem inventory are refused.

Use --dry-run. Automatic deletion is disabled until inventory and deletion can
share lifecycle fencing; a preview is not authorization to delete resources.`,
	RunE: runGc,
}

func init() {
	gcCmd.Flags().StringVar(&flagGcRunsDir, "runs", config.DefaultRunsDir(), "directory of per-topology state files")
	gcCmd.Flags().BoolVar(&flagGcDryRun, "dry-run", false, "preview scoped candidates without removing them (required)")
}

var errGcDeletionDisabled = errors.New("automatic GC deletion is disabled: inventory and deletion do not share lifecycle fencing; use --dry-run")

func runGc(cmd *cobra.Command, _ []string) error {
	if !flagGcDryRun {
		return errGcDeletionDisabled
	}
	if flagBackend != "" || strings.Contains(flagStateFile, "://") {
		return fmt.Errorf("refusing GC: configured backend is not covered by the --runs filesystem inventory")
	}
	ctx := cmd.Context()
	active, err := collectActiveIDsChecked(flagGcRunsDir)
	if err != nil {
		return fmt.Errorf("refusing GC: active resource inventory is incomplete: %w", err)
	}

	output := cmd.OutOrStdout()
	fmt.Fprintf(output, "Preview scope: canonical filesystem state under %s only; SQL/remote backends and other runs directories are not inventoried.\n", flagGcRunsDir)
	orphans, err := findOrphansWithReport(ctx, active, output)
	if err != nil {
		return err
	}
	if len(orphans) == 0 {
		fmt.Fprintln(output, "No unreferenced candidates found within the inventoried owner scope; nothing removed.")
		return nil
	}
	for _, o := range orphans {
		fmt.Fprintln(output, o)
	}
	fmt.Fprintf(output, "\n%d unreferenced candidate(s) in the inventoried owner scope (dry-run; nothing removed). Preview is not a deletion authorization.\n", len(orphans))
	return nil
}

type activeIDs struct {
	owners      map[string]bool
	inventories map[string]string
	containers  map[string]bool
	networks    map[string]bool
	domains     map[string]bool
}

// collectActiveIDs walks runsDir/*/state.json and collects the external IDs of
// every live resource, grouped by substrate. Legacy state protects known IDs,
// but cannot authorize an ownership scope without a canonical action inventory.
func collectActiveIDs(runsDir string) activeIDs {
	a := activeIDs{
		containers: map[string]bool{},
		networks:   map[string]bool{},
		domains:    map[string]bool{},
	}
	files, _ := filepath.Glob(filepath.Join(runsDir, "*", "state.json"))
	for _, f := range files {
		st, err := state.NewManager(f).Load()
		if err != nil {
			continue
		}
		for i := range st.Resources {
			r := &st.Resources[i]
			switch r.Address.Type {
			case "sysbox_network":
				a.networks[r.ExternalID] = true
			case "sysbox_node", "sysbox_router":
				switch r.Driver {
				case "docker":
					a.containers[r.ExternalID] = true
				case "libvirt":
					a.domains[r.ExternalID] = true
				}
			}
		}
	}
	return a
}

func collectActiveIDsChecked(runsDir string) (activeIDs, error) {
	active := activeIDs{owners: map[string]bool{}, inventories: map[string]string{}, containers: map[string]bool{}, networks: map[string]bool{}, domains: map[string]bool{}}
	if _, err := os.ReadDir(runsDir); err != nil {
		if os.IsNotExist(err) {
			return active, nil
		}
		return active, err
	}
	aggregates, err := filepath.Glob(filepath.Join(runsDir, "*", "state.json.lifecycle"))
	if err != nil {
		return active, err
	}
	for _, root := range aggregates {
		topology := filepath.Base(filepath.Dir(root))
		if info, err := os.Lstat(filepath.Join(root, topology+".json")); err != nil {
			return active, fmt.Errorf("canonical inventory %s unavailable: %w", topology, err)
		} else if !info.Mode().IsRegular() {
			return active, fmt.Errorf("canonical inventory %s is not a regular aggregate", topology)
		}
		repo, err := persistence.OpenFilesystem(root)
		if err != nil {
			return active, err
		}
		actions, err := repo.LoadActions(context.Background(), topology)
		if err != nil {
			repo.Close()
			return active, err
		}
		for _, action := range actions {
			if action.Status != runtime.ActionSucceeded {
				repo.Close()
				return active, fmt.Errorf("topology %s has unresolved action %s", topology, action.ID)
			}
		}
		st, err := repo.LoadState(context.Background(), topology)
		if err != nil {
			repo.Close()
			return active, err
		}
		retained, err := repo.SnapshotResources(context.Background(), topology)
		if err != nil {
			repo.Close()
			return active, err
		}
		for _, r := range retained {
			switch r.Address.Type {
			case "sysbox_network":
				active.networks[r.ExternalID] = true
			case "sysbox_node", "sysbox_router":
				if r.Driver == "docker" {
					active.containers[r.ExternalID] = true
				}
				if r.Driver == "libvirt" {
					active.domains[r.ExternalID] = true
				}
			}
		}
		repo.Close()
		if st.TopologyUID == "" {
			return active, fmt.Errorf("canonical topology %s lacks durable ownership", topology)
		}
		for _, action := range actions {
			if action.OwnerUID != st.TopologyUID {
				return active, fmt.Errorf("canonical topology %s has conflicting action ownership", topology)
			}
		}
		active.owners[st.TopologyUID] = true
		inventoryRaw, err := json.Marshal(struct {
			TopologyUID string
			State       *state.State
			Actions     []runtime.ActionRecord
			Snapshots   []state.Resource
		}{st.TopologyUID, st, actions, retained})
		if err != nil {
			return active, err
		}
		active.inventories[st.TopologyUID] = fmt.Sprintf("%x", sha256.Sum256(inventoryRaw))
		for _, r := range st.Resources {
			if r.OwnerUID != st.TopologyUID || r.ResourceUID == "" || r.Generation < 1 {
				return active, fmt.Errorf("canonical topology %s has incomplete resource ownership", topology)
			}
			switch r.Address.Type {
			case "sysbox_network":
				active.networks[r.ExternalID] = true
			case "sysbox_node", "sysbox_router":
				if r.Driver == "docker" {
					active.containers[r.ExternalID] = true
				}
				if r.Driver == "libvirt" {
					active.domains[r.ExternalID] = true
				}
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(runsDir, "*", "state.json"))
	if err != nil {
		return active, err
	}
	for _, f := range files {
		st, err := state.NewManager(f).Load()
		if err != nil {
			return active, fmt.Errorf("read %s: %w", f, err)
		}
		for _, r := range st.Resources {
			switch r.Address.Type {
			case "sysbox_network":
				active.networks[r.ExternalID] = true
			case "sysbox_node", "sysbox_router":
				if r.Driver == "docker" {
					active.containers[r.ExternalID] = true
				}
				if r.Driver == "libvirt" {
					active.domains[r.ExternalID] = true
				}
			}
		}
	}
	return active, nil
}

type orphan struct {
	Kind            string // container | network | domain
	ID              string
	Topo            string
	OwnerUID        string
	ResourceUID     string
	Generation      int64
	InventoryDigest string
}

func (o orphan) String() string {
	if o.Topo != "" {
		return fmt.Sprintf("%-9s %s  (topology=%s owner=%s resource=%s generation=%d inventory=%s)", o.Kind, o.ID, o.Topo, o.OwnerUID, o.ResourceUID, o.Generation, o.InventoryDigest)
	}
	return fmt.Sprintf("%-9s %s", o.Kind, o.ID)
}

func findOrphans(ctx context.Context, active activeIDs) ([]orphan, error) {
	return findOrphansWithReport(ctx, active, io.Discard)
}

func gcOwnerAllowed(active activeIDs, owner, topology, resource string, generation int64) bool {
	return owner != "" && active.owners[owner] && topology == owner && resource != "" && generation > 0
}

func findOrphansWithReport(ctx context.Context, active activeIDs, report io.Writer) ([]orphan, error) {
	if len(active.owners) == 0 {
		fmt.Fprintln(report, "skipped host scan: no reliable canonical OwnerUID was inventoried")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out []orphan
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		fmt.Fprintf(report, "skipped Docker: client unavailable: %v\n", err)
	} else {
		defer cli.Close()
		if containers, err := cli.ContainerList(ctx, container.ListOptions{All: true}); err != nil {
			fmt.Fprintf(report, "skipped Docker containers: inventory unavailable: %v\n", err)
		} else {
			for _, c := range containers {
				if c.Labels[runtime.LabelManaged] != "true" {
					continue
				}
				generation, _ := strconv.ParseInt(c.Labels["sysbox.generation"], 10, 64)
				if !gcOwnerAllowed(active, c.Labels["sysbox.owner_uid"], c.Labels["sysbox.topology_uid"], c.Labels["sysbox.resource_uid"], generation) {
					fmt.Fprintf(report, "skipped container %s: durable owner is not inventoried or identity is incomplete\n", c.ID)
					continue
				}
				if !active.containers[c.ID] {
					out = append(out, orphan{Kind: "container", ID: c.ID, Topo: c.Labels[runtime.LabelTopology], OwnerUID: c.Labels["sysbox.owner_uid"], ResourceUID: c.Labels["sysbox.resource_uid"], Generation: generation, InventoryDigest: active.inventories[c.Labels["sysbox.owner_uid"]]})
				}
			}
		}
		if networks, err := cli.NetworkList(ctx, network.ListOptions{}); err != nil {
			fmt.Fprintf(report, "skipped Docker networks: inventory unavailable: %v\n", err)
		} else {
			for _, n := range networks {
				if n.Labels[runtime.LabelManaged] != "true" {
					continue
				}
				generation, _ := strconv.ParseInt(n.Labels["sysbox.generation"], 10, 64)
				if !gcOwnerAllowed(active, n.Labels["sysbox.owner_uid"], n.Labels["sysbox.topology_uid"], n.Labels["sysbox.resource_uid"], generation) {
					fmt.Fprintf(report, "skipped network %s: durable owner is not inventoried or identity is incomplete\n", n.ID)
					continue
				}
				if !active.networks[n.ID] {
					out = append(out, orphan{Kind: "network", ID: n.ID, Topo: n.Labels[runtime.LabelTopology], OwnerUID: n.Labels["sysbox.owner_uid"], ResourceUID: n.Labels["sysbox.resource_uid"], Generation: generation, InventoryDigest: active.inventories[n.Labels["sysbox.owner_uid"]]})
				}
			}
		}
	}
	domains, err := findOrphanDomains(ctx, active, report)
	if err != nil {
		return nil, err
	}
	out = append(out, domains...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type gcDomain struct {
	UUID     string `xml:"uuid"`
	Metadata struct {
		Managed *struct {
			OwnerUID    string `xml:"owner_uid,attr"`
			ResourceUID string `xml:"resource_uid,attr"`
			TopologyUID string `xml:"topology_uid,attr"`
			Generation  int64  `xml:"generation,attr"`
		} `xml:"urn:sysbox:managed managed"`
	} `xml:"metadata"`
}

func gcVirshOutput(ctx context.Context, args ...string) ([]byte, error) {
	stdout, stderr := substrate.NewExecCapture(1<<20, nil), substrate.NewExecCapture(64<<10, nil)
	command := exec.CommandContext(ctx, "virsh", args...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.Truncated() || stderr.Truncated() {
		return nil, fmt.Errorf("virsh response exceeded inventory limit")
	}
	return []byte(stdout.String()), nil
}

func findOrphanDomains(ctx context.Context, active activeIDs, report io.Writer) ([]orphan, error) {
	raw, err := gcVirshOutput(ctx, "list", "--all", "--uuid")
	if err != nil {
		fmt.Fprintf(report, "skipped libvirt domains: inventory unavailable: %v\n", err)
		return nil, nil
	}
	ids := strings.Fields(string(raw))
	if len(ids) > 1024 {
		return nil, fmt.Errorf("refusing GC preview: libvirt inventory exceeds 1024 domains")
	}
	var out []orphan
	for _, id := range ids {
		raw, err := gcVirshOutput(ctx, "dumpxml", id)
		if err != nil {
			fmt.Fprintf(report, "skipped domain %s: ownership metadata unavailable: %v\n", id, err)
			continue
		}
		var domain gcDomain
		if err := xml.Unmarshal(raw, &domain); err != nil {
			fmt.Fprintf(report, "skipped domain %s: invalid ownership metadata\n", id)
			continue
		}
		owner := domain.Metadata.Managed
		if domain.UUID != id || owner == nil || !gcOwnerAllowed(active, owner.OwnerUID, owner.TopologyUID, owner.ResourceUID, owner.Generation) {
			fmt.Fprintf(report, "skipped domain %s: durable owner is not inventoried or identity is incomplete\n", id)
			continue
		}
		if !active.domains[id] {
			out = append(out, orphan{Kind: "domain", ID: id, Topo: owner.TopologyUID, OwnerUID: owner.OwnerUID, ResourceUID: owner.ResourceUID, Generation: owner.Generation, InventoryDigest: active.inventories[owner.OwnerUID]})
		}
	}
	return out, nil
}

// A dry-run snapshot cannot fence a later create/adopt operation. Keep the
// deletion entry point disabled even if called with a previously reviewed list.
func removeOrphans(context.Context, []orphan) error { return errGcDeletionDisabled }
