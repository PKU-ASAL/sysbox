package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/spf13/cobra"
)

func newGCCacheCommand() *cobra.Command {
	var catalog, root, path, apply, reconcile, recoverIntent string
	cmd := &cobra.Command{Use: "cache", Short: "Plan or execute one fenced private-cache deletion", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&catalog, "catalog", "", "exclusive shared SQLite/PostgreSQL lifecycle catalog for this cache")
	cmd.Flags().StringVar(&root, "cache-root", "", "absolute private host cache root (all writers must be enrolled)")
	cmd.Flags().StringVar(&path, "path", "", "relative cache key/file to inspect")
	cmd.Flags().StringVar(&apply, "apply-plan", "", "execute exact JSON plan file; default is read-only planning")
	cmd.Flags().StringVar(&reconcile, "reconcile-plan", "", "release PostgreSQL reservation from exact durable done receipt; never delete")
	cmd.Flags().StringVar(&recoverIntent, "recover-intent-plan", "", "complete exact absent-file intent after acquiring original host deletion lock; never delete")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		location, _, err := gcCatalogLocation(catalog)
		if err != nil {
			return err
		}
		repo, err := persistence.OpenImportSource(cmd.Context(), location, "")
		if err != nil {
			return err
		}
		defer repo.Close()
		selected := 0
		for _, v := range []string{apply, reconcile, recoverIntent} {
			if v != "" {
				selected++
			}
		}
		if selected > 1 {
			return fmt.Errorf("apply-plan, reconcile-plan and recover-intent-plan are mutually exclusive")
		}
		if selected > 0 {
			if root != "" || path != "" {
				return fmt.Errorf("apply-plan cannot override root or path")
			}
			planFile := apply
			if reconcile != "" {
				planFile = reconcile
			}
			if recoverIntent != "" {
				planFile = recoverIntent
			}
			f, err := os.Open(planFile)
			if err != nil {
				return err
			}
			defer f.Close()
			decoder := json.NewDecoder(io.LimitReader(f, 65536))
			decoder.DisallowUnknownFields()
			var plan persistence.CacheGCPlan
			if err = decoder.Decode(&plan); err != nil {
				return err
			}
			if err = decoder.Decode(new(any)); err != io.EOF {
				return fmt.Errorf("one plan required")
			}
			if recoverIntent != "" {
				err = repo.RecoverCacheGCIntent(cmd.Context(), plan)
			} else if reconcile != "" {
				err = repo.ReconcileCacheGC(cmd.Context(), plan)
			} else {
				err = repo.ApplyCacheGC(cmd.Context(), plan)
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"id": plan.ID, "deleted": true, "reconciled": reconcile != "" || recoverIntent != "", "receipt": plan.Root + "/.sysbox-gc/" + plan.ID + ".done"})
		}
		plan, err := repo.PlanCacheGC(cmd.Context(), root, path)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
	}
	return cmd
}
func init() { gcCmd.AddCommand(newGCCacheCommand()) }
