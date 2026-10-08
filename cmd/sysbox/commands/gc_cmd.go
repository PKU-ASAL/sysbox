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

	"github.com/gofrs/flock"

	types "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

var (
	flagGcRunsDir string
	flagGcDryRun  bool
	flagGcExecute bool
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
	gcCmd.Flags().BoolVar(&flagGcDryRun, "dry-run", false, "preview scoped candidates without removing them")
	gcCmd.Flags().BoolVar(&flagGcExecute, "execute", false, "execute fenced provider deletes; requires canonical inventory revalidation")
}

var errGcDeletionDisabled = errors.New("automatic GC deletion is disabled: inventory and deletion do not share lifecycle fencing; use --dry-run")

func runGc(cmd *cobra.Command, _ []string) error {
	if !flagGcDryRun && !flagGcExecute {
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
	if flagGcExecute {
		if flagGcDryRun {
			return fmt.Errorf("refusing GC: --dry-run and --execute are mutually exclusive")
		}
		if err := executeOrphans(ctx, flagGcRunsDir, orphans, output); err != nil {
			return err
		}
		fmt.Fprintf(output, "\n%d candidate(s) deleted with durable fencing and post-delete verification.\n", len(orphans))
		return nil
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
// legacy helper disabled; execution goes through executeOrphans below.
func removeOrphans(context.Context, []orphan) error { return errGcDeletionDisabled }

type gcJournalRecord struct {
	Candidate orphan    `json:"candidate"`
	Digest    string    `json:"digest"`
	Status    string    `json:"status"` // intent | complete | uncertain
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type gcJournal struct {
	Version int                        `json:"version"`
	Records map[string]gcJournalRecord `json:"records"`
}

func executeOrphans(ctx context.Context, runsDir string, candidates []orphan, report io.Writer) error {
	if len(candidates) == 0 {
		return nil
	}
	journalDir := filepath.Join(runsDir, "_gc")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(journalDir, "deletion-journal.json")
	lock := flock.New(path + ".lock")
	ok, err := lock.TryLockContext(ctx, 10*time.Second)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("GC deletion journal is locked")
		}
		return err
	}
	defer lock.Unlock()
	j, err := loadGCJournal(path)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		key := candidate.Kind + ":" + candidate.ID
		digest := gcCandidateDigest(candidate)
		if prior, ok := j.Records[key]; ok {
			if prior.Digest != digest || prior.Candidate.OwnerUID != candidate.OwnerUID || prior.Candidate.ResourceUID != candidate.ResourceUID || prior.Candidate.Generation != candidate.Generation {
				return fmt.Errorf("GC candidate %s identity changed", key)
			}
			switch prior.Status {
			case "complete":
				continue
			case "intent", "uncertain":
				return fmt.Errorf("GC candidate %s: %w", key, effectgate.ErrUncertain)
			default:
				return fmt.Errorf("GC candidate %s has invalid journal status", key)
			}
		}
		current, err := collectActiveIDsChecked(runsDir)
		if err != nil {
			return fmt.Errorf("GC inventory changed or became incomplete: %w", err)
		}
		if candidate.OwnerUID == "" || current.inventories[candidate.OwnerUID] == "" || current.inventories[candidate.OwnerUID] != candidate.InventoryDigest {
			return fmt.Errorf("GC candidate %s inventory digest changed", key)
		}
		if candidate.Kind != "container" && candidate.Kind != "network" && candidate.Kind != "domain" {
			// Preserve the intent and uncertain record for an unsupported provider
			// kind; an unknown kind can never be silently treated as absent.
		} else if activeContains(current, candidate) {
			return fmt.Errorf("GC candidate %s is referenced again", key)
		}
		j.Records[key] = gcJournalRecord{Candidate: candidate, Digest: digest, Status: "intent", UpdatedAt: time.Now().UTC()}
		if err := saveGCJournal(path, j); err != nil {
			return err
		}
		if err := deleteProviderCandidate(ctx, candidate); err != nil {
			j.Records[key] = gcJournalRecord{Candidate: candidate, Digest: digest, Status: "uncertain", Error: err.Error(), UpdatedAt: time.Now().UTC()}
			_ = saveGCJournal(path, j)
			return fmt.Errorf("GC delete %s: %w", key, err)
		}
		if err := verifyProviderCandidateAbsent(ctx, candidate); err != nil {
			j.Records[key] = gcJournalRecord{Candidate: candidate, Digest: digest, Status: "uncertain", Error: err.Error(), UpdatedAt: time.Now().UTC()}
			_ = saveGCJournal(path, j)
			return fmt.Errorf("GC verify %s: %w", key, err)
		}
		j.Records[key] = gcJournalRecord{Candidate: candidate, Digest: digest, Status: "complete", UpdatedAt: time.Now().UTC()}
		if err := saveGCJournal(path, j); err != nil {
			return err
		}
		fmt.Fprintf(report, "deleted %s\n", candidate)
	}
	return nil
}

func gcCandidateDigest(candidate orphan) string {
	raw, _ := json.Marshal(candidate)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func loadGCJournal(path string) (gcJournal, error) {
	j := gcJournal{Version: 1, Records: map[string]gcJournalRecord{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal(raw, &j); err != nil || j.Version != 1 || j.Records == nil {
		return j, fmt.Errorf("GC deletion journal requires explicit rebuild")
	}
	return j, nil
}

func saveGCJournal(path string, journal gcJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func activeContains(active activeIDs, candidate orphan) bool {
	switch candidate.Kind {
	case "container":
		return active.containers[candidate.ID]
	case "network":
		return active.networks[candidate.ID]
	case "domain":
		return active.domains[candidate.ID]
	default:
		return true
	}
}

func deleteProviderCandidate(ctx context.Context, candidate orphan) error {
	switch candidate.Kind {
	case "container", "network":
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return err
		}
		defer cli.Close()
		if candidate.Kind == "container" {
			inspected, inspectErr := cli.ContainerInspect(ctx, candidate.ID)
			if errdefs.IsNotFound(inspectErr) {
				return nil
			}
			if err := validateGCContainer(candidate, inspected, inspectErr); err != nil {
				return err
			}
			err = cli.ContainerRemove(ctx, inspected.ID, container.RemoveOptions{Force: true})
		} else {
			inspected, inspectErr := cli.NetworkInspect(ctx, candidate.ID, network.InspectOptions{})
			if errdefs.IsNotFound(inspectErr) {
				return nil
			}
			if err := validateGCNetwork(candidate, inspected, inspectErr); err != nil {
				return err
			}
			err = cli.NetworkRemove(ctx, inspected.ID)
		}
		if err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		return nil
	case "domain":
		raw, err := gcVirshOutput(ctx, "dumpxml", candidate.ID)
		if err != nil {
			return fmt.Errorf("virsh ownership check: %w", err)
		}
		var domain gcDomain
		if err := xml.Unmarshal(raw, &domain); err != nil {
			return fmt.Errorf("virsh ownership check: %w", err)
		}
		if err := validateGCDomain(candidate, domain); err != nil {
			return err
		}
		if out, err := exec.CommandContext(ctx, "virsh", "destroy", domain.UUID).CombinedOutput(); err != nil && !strings.Contains(string(out), "not running") {
			return fmt.Errorf("virsh destroy: %w: %s", err, strings.TrimSpace(string(out)))
		}
		// Re-check immutable identity and managed ownership immediately before undefine.
		raw, err = gcVirshOutput(ctx, "dumpxml", domain.UUID)
		if err != nil {
			return fmt.Errorf("virsh ownership check before undefine: %w", err)
		}
		var current gcDomain
		if err := xml.Unmarshal(raw, &current); err != nil {
			return fmt.Errorf("virsh ownership check before undefine: %w", err)
		}
		if err := validateGCDomain(candidate, current); err != nil {
			return err
		}
		if out, err := exec.CommandContext(ctx, "virsh", "undefine", domain.UUID).CombinedOutput(); err != nil && !strings.Contains(string(out), "not found") {
			return fmt.Errorf("virsh undefine: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	default:
		return fmt.Errorf("unsupported GC provider kind %q", candidate.Kind)
	}
}

func validateGCContainer(candidate orphan, inspected types.ContainerJSON, err error) error {
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("container ownership check: %w", err)
	}
	if inspected.Config == nil {
		return fmt.Errorf("container %s ownership metadata unavailable", candidate.ID)
	}
	labels := inspected.Config.Labels
	if inspected.ID != candidate.ID || !gcLabelsMatch(candidate, labels) {
		return fmt.Errorf("container %s identity or ownership changed", candidate.ID)
	}
	return nil
}

func validateGCNetwork(candidate orphan, inspected network.Inspect, err error) error {
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("network ownership check: %w", err)
	}
	if inspected.ID != candidate.ID || !gcLabelsMatch(candidate, inspected.Labels) {
		return fmt.Errorf("network %s identity or ownership changed", candidate.ID)
	}
	return nil
}

func gcLabelsMatch(candidate orphan, labels map[string]string) bool {
	if labels == nil || labels[runtime.LabelManaged] != "true" {
		return false
	}
	return labels["sysbox.owner_uid"] == candidate.OwnerUID && labels["sysbox.topology_uid"] == candidate.OwnerUID && labels["sysbox.resource_uid"] == candidate.ResourceUID && labels["sysbox.generation"] == strconv.FormatInt(candidate.Generation, 10)
}

func validateGCDomain(candidate orphan, domain gcDomain) error {
	owner := domain.Metadata.Managed
	if domain.UUID != candidate.ID || owner == nil || owner.OwnerUID != candidate.OwnerUID || owner.TopologyUID != candidate.OwnerUID || owner.ResourceUID != candidate.ResourceUID || owner.Generation != candidate.Generation || !gcOwnerAllowed(activeIDs{owners: map[string]bool{candidate.OwnerUID: true}}, owner.OwnerUID, owner.TopologyUID, owner.ResourceUID, owner.Generation) {
		return fmt.Errorf("domain %s identity or ownership changed", candidate.ID)
	}
	return nil
}

func verifyProviderCandidateAbsent(ctx context.Context, candidate orphan) error {
	switch candidate.Kind {
	case "container", "network":
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return err
		}
		defer cli.Close()
		if candidate.Kind == "container" {
			_, err = cli.ContainerInspect(ctx, candidate.ID)
		} else {
			_, err = cli.NetworkInspect(ctx, candidate.ID, network.InspectOptions{})
		}
		if err == nil {
			return fmt.Errorf("provider still reports %s", candidate.ID)
		}
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	case "domain":
		ids, err := gcVirshOutput(ctx, "list", "--all", "--uuid")
		if err != nil {
			return err
		}
		if strings.Contains(string(ids), candidate.ID) {
			return fmt.Errorf("provider still reports %s", candidate.ID)
		}
		return nil
	default:
		return fmt.Errorf("unsupported GC provider kind %q", candidate.Kind)
	}
}
