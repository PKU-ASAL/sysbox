package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/spf13/cobra"
)

type gcBatchPlan struct {
	CatalogID        string                     `json:"catalog_id,omitempty"`
	Complete         bool                       `json:"complete"`
	Scope            string                     `json:"scope"`
	Topologies       map[string]string          `json:"topologies"`
	Inventory        []controlplane.GCCandidate `json:"inventory"`
	Exclusions       string                     `json:"exclusions,omitempty"`
	Version          int                        `json:"version"`
	RunsDir          string                     `json:"runs_dir"`
	CreatedAt        time.Time                  `json:"created_at"`
	Candidates       []controlplane.GCCandidate `json:"candidates"`
	InventoryDigests map[string]string          `json:"inventory_digests"`
}

func newGCPlanCommand() *cobra.Command {
	var runs, previous, catalog string
	var retention time.Duration
	var limit int
	cmd := &cobra.Command{SilenceUsage: true, Use: "plan", Short: "Write a machine-readable, read-only GC candidate batch plan", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&runs, "runs", config.DefaultRunsDir(), "directory of canonical lifecycle aggregates")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if catalog == "" && (flagBackend != "" || strings.Contains(flagStateFile, "://")) {
			return fmt.Errorf("plan scope is local filesystem; remote/SQL backend requires owning-host inventory")
		}
		if retention < 0 || limit < 1 || limit > 128 {
			return fmt.Errorf("retention must be nonnegative and limit 1..128")
		}
		var active activeIDs
		var err error
		catalogID := ""
		if catalog != "" {
			_, catalogID, err = gcCatalogLocation(catalog)
			if err != nil {
				return err
			}
			active, err = collectCatalogIDs(cmd.Context(), catalog)
		} else {
			active, err = collectActiveIDsChecked(runs)
		}
		if err != nil {
			return err
		}
		var diagnostics bytes.Buffer
		candidates, err := findOrphansWithReport(cmd.Context(), active, &diagnostics)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(diagnostics.String()), "\n") {
			if line != "" && !strings.Contains(line, "durable owner is not inventoried or identity is incomplete") && !strings.Contains(line, "no reliable canonical OwnerUID") {
				return fmt.Errorf("incomplete provider inventory: %s", line)
			}
		}
		plan := gcBatchPlan{CatalogID: catalogID, Complete: true, Scope: "local-filesystem-and-host", Topologies: active.topologies, Exclusions: diagnostics.String(), Version: 1, RunsDir: runs, CreatedAt: time.Now().UTC(), InventoryDigests: map[string]string{}}
		for owner, digest := range active.inventories {
			plan.InventoryDigests[owner] = digest
		}
		for _, o := range candidates {
			plan.Inventory = append(plan.Inventory, controlplane.GCCandidate{Kind: o.Kind, ExternalID: o.ID, TopologyUID: o.OwnerUID, OwnerUID: o.OwnerUID, ResourceUID: o.ResourceUID, Generation: o.Generation, InventoryDigest: o.InventoryDigest})
		}
		var prior *gcBatchPlan
		if previous != "" {
			file, err := os.Open(previous)
			if err != nil {
				return err
			}
			defer file.Close()
			var old gcBatchPlan
			decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&old); err != nil {
				return err
			}
			if old.RunsDir != runs || old.Scope != plan.Scope || old.CatalogID != plan.CatalogID {
				return fmt.Errorf("previous plan scope differs")
			}
			prior = &old
		}
		plan.Candidates, err = selectGCCandidates(plan.Inventory, prior, retention, limit, plan.CreatedAt)
		if err != nil {
			return err
		}
		if plan.Inventory == nil {
			plan.Inventory = []controlplane.GCCandidate{}
		}

		raw, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		output := cmd.OutOrStdout()
		if path := cmd.Flag("output").Value.String(); path != "" && path != "-" {
			if err := os.WriteFile(filepath.Clean(path), raw, 0600); err != nil {
				return err
			}
			_, err = fmt.Fprintf(output, "wrote %d GC candidates to %s\n", len(plan.Candidates), path)
			return err
		}
		_, err = fmt.Fprintln(output, string(raw))
		return err
	}
	cmd.Flags().StringVar(&catalog, "catalog", "", "shared canonical SQLite/PostgreSQL catalog; run on its owning resource host")
	cmd.Flags().StringVar(&previous, "previous-plan", "", "earlier complete inventory for retention proof")
	cmd.Flags().DurationVar(&retention, "min-age", 24*time.Hour, "minimum time seen unreferenced in two inventories")
	cmd.Flags().IntVar(&limit, "limit", 32, "maximum candidates selected (1..128)")
	cmd.Flags().String("output", "-", "plan output path, or - for stdout")
	return cmd
}

func init() { gcCmd.AddCommand(newGCPlanCommand()) }

func selectGCCandidates(current []controlplane.GCCandidate, prior *gcBatchPlan, age time.Duration, limit int, now time.Time) ([]controlplane.GCCandidate, error) {
	if age < 0 || limit < 1 || limit > 128 {
		return nil, fmt.Errorf("invalid GC selection policy")
	}
	old := map[string]bool{}
	identity := func(c controlplane.GCCandidate) string {
		c.InventoryDigest = ""
		raw, _ := json.Marshal(c)
		return string(raw)
	}
	if prior != nil {
		if !prior.Complete || prior.Version != 1 || prior.CreatedAt.IsZero() || prior.CreatedAt.After(now) {
			return nil, fmt.Errorf("invalid previous inventory")
		}
		if now.Sub(prior.CreatedAt) >= age {
			items := prior.Inventory
			if items == nil {
				items = prior.Candidates
			}
			for _, c := range items {
				old[identity(c)] = true
			}
		}
	}
	result := []controlplane.GCCandidate{}
	seen := map[string]bool{}
	for _, c := range current {
		if err := c.Validate(); err != nil {
			return nil, err
		}
		key := identity(c)
		if seen[key] {
			return nil, fmt.Errorf("duplicate provider identity")
		}
		seen[key] = true
		if age == 0 || old[key] {
			result = append(result, c)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if (a.Kind == "network") != (b.Kind == "network") {
			return b.Kind == "network"
		}
		if a.TopologyUID != b.TopologyUID {
			return a.TopologyUID < b.TopologyUID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ExternalID < b.ExternalID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
