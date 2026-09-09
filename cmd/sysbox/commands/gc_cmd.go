package commands

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

var (
	flagGcRunsDir string
	flagGcDryRun  bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Reclaim orphaned sandbox resources (docker containers/networks, libvirt VMs)",
	Long: `Scan for sysbox-managed objects that are no longer referenced by any active
topology state, and remove them.

Orphans arise when a rollout is interrupted (Ctrl+C / SIGKILL) before its
destroy runs. Only objects carrying the sysbox.managed label (docker) or the
sysbox-managed title (libvirt) are considered, so other experiments are never
touched. Run with --dry-run first to review what would be removed.`,
	RunE: runGc,
}

func init() {
	gcCmd.Flags().StringVar(&flagGcRunsDir, "runs", config.DefaultRunsDir(), "directory of per-topology state files")
	gcCmd.Flags().BoolVar(&flagGcDryRun, "dry-run", false, "print orphans without removing them")
}

func runGc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	active := collectActiveIDs(flagGcRunsDir)

	orphans, err := findOrphans(ctx, active)
	if err != nil {
		return err
	}
	if len(orphans) == 0 {
		fmt.Println("No orphaned resources found.")
		return nil
	}
	for _, o := range orphans {
		fmt.Println(o)
	}
	if flagGcDryRun {
		fmt.Printf("\n%d orphan(s) found (dry-run; nothing removed).\n", len(orphans))
		return nil
	}

	if err := removeOrphans(ctx, orphans); err != nil {
		return err
	}
	fmt.Printf("\nRemoved %d orphan(s).\n", len(orphans))
	return nil
}

type activeIDs struct {
	containers map[string]bool
	networks   map[string]bool
	domains    map[string]bool
}

// collectActiveIDs walks runsDir/*/state.json and collects the external IDs of
// every live resource, grouped by substrate. An object on the host whose ID is
// absent from the matching group is an orphan.
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

type orphan struct {
	Kind string // container | network | domain
	ID   string
	Topo string
}

func (o orphan) String() string {
	if o.Topo != "" {
		return fmt.Sprintf("%-9s %s  (topology=%s)", o.Kind, o.ID, o.Topo)
	}
	return fmt.Sprintf("%-9s %s", o.Kind, o.ID)
}

func findOrphans(ctx context.Context, active activeIDs) ([]orphan, error) {
	var out []orphan

	// Docker: containers and networks are identified by the sysbox.managed
	// label. This part degrades gracefully when the docker daemon is unreachable.
	if cli, err := client.NewClientWithOpts(client.FromEnv); err == nil {
		if containers, listErr := cli.ContainerList(ctx, container.ListOptions{All: true}); listErr == nil {
			for _, c := range containers {
				if c.Labels[runtime.LabelManaged] != "true" {
					continue
				}
				if !active.containers[c.ID] {
					out = append(out, orphan{Kind: "container", ID: c.ID, Topo: c.Labels[runtime.LabelTopology]})
				}
			}
		}
		if nets, listErr := cli.NetworkList(ctx, network.ListOptions{}); listErr == nil {
			for _, n := range nets {
				if n.Labels[runtime.LabelManaged] != "true" {
					continue
				}
				if !active.networks[n.ID] {
					out = append(out, orphan{Kind: "network", ID: n.ID, Topo: n.Labels[runtime.LabelTopology]})
				}
			}
		}
		_ = cli.Close()
	}

	// libvirt: domains carry a "sysbox-managed" title. A host without libvirt
	// simply yields no domains.
	out = append(out, findOrphanDomains(ctx, active.domains)...)

	return out, nil
}

func findOrphanDomains(ctx context.Context, active map[string]bool) []orphan {
	raw, err := exec.CommandContext(ctx, "virsh", "list", "--all", "--name").Output()
	if err != nil {
		return nil
	}
	var out []orphan
	for _, name := range strings.Fields(string(raw)) {
		if !sysboxManagedDomain(ctx, name) {
			continue
		}
		if !active[name] {
			out = append(out, orphan{Kind: "domain", ID: name})
		}
	}
	return out
}

// sysboxManagedDomain reports whether a libvirt domain was created by sysbox,
// mirroring libvirt.isSysboxManaged (the "sysbox-managed" title metadata).
func sysboxManagedDomain(ctx context.Context, name string) bool {
	out, err := exec.CommandContext(ctx, "virsh", "dominfo", name).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "sysbox-managed")
}

func removeOrphans(ctx context.Context, orphans []orphan) error {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}
	defer cli.Close()

	// Containers before networks: a bridge network with an attached container
	// cannot be removed.
	for _, o := range orphans {
		if o.Kind != "container" {
			continue
		}
		if err := cli.ContainerRemove(ctx, o.ID, container.RemoveOptions{Force: true}); err != nil {
			fmt.Printf("warn: remove container %s: %v\n", o.ID, err)
			continue
		}
		fmt.Printf("removed container %s (%s)\n", o.ID, o.Topo)
	}
	for _, o := range orphans {
		if o.Kind != "network" {
			continue
		}
		if err := cli.NetworkRemove(ctx, o.ID); err != nil {
			fmt.Printf("warn: remove network %s: %v\n", o.ID, err)
			continue
		}
		fmt.Printf("removed network %s (%s)\n", o.ID, o.Topo)
	}
	for _, o := range orphans {
		if o.Kind != "domain" {
			continue
		}
		_ = exec.CommandContext(ctx, "virsh", "destroy", o.ID).Run()
		if err := exec.CommandContext(ctx, "virsh", "undefine", o.ID).Run(); err != nil {
			fmt.Printf("warn: undefine domain %s: %v\n", o.ID, err)
			continue
		}
		fmt.Printf("removed domain %s\n", o.ID)
	}
	return nil
}
