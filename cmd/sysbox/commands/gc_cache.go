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
	var catalog, root, path, apply string
	cmd := &cobra.Command{Use: "cache", Short: "Plan cache GC; execute one private-cache deletion with local SQLite", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&catalog, "catalog", "", "exclusive shared SQLite/PostgreSQL lifecycle catalog for this cache")
	cmd.Flags().StringVar(&root, "cache-root", "", "absolute private host cache root (all writers must be enrolled)")
	cmd.Flags().StringVar(&path, "path", "", "relative cache key/file to inspect")
	cmd.Flags().StringVar(&apply, "apply-plan", "", "execute exact JSON plan file; default is read-only planning")
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
		if apply != "" {
			if root != "" || path != "" {
				return fmt.Errorf("apply-plan cannot override root or path")
			}
			f, err := os.Open(apply)
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
			if err = repo.ApplyCacheGC(cmd.Context(), plan); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"id": plan.ID, "deleted": true, "receipt": plan.Root + "/.sysbox-gc/" + plan.ID + ".done"})
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
