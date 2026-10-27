package commands

import (
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/spf13/cobra"
)

func newStateImportFormat4Command() *cobra.Command { return newStateImportCommand(4) }
func newStateImportFormat5Command() *cobra.Command { return newStateImportCommand(5) }
func newStateImportCommand(format int) *cobra.Command {
	var apply, offline bool
	var sourceSHA256, archive string
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("import-format%d", format),
		Short: fmt.Sprintf("Preview or explicitly import a stopped format %d lifecycle repository", format),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if apply && (!offline || sourceSHA256 == "" || archive == "") {
				return fmt.Errorf("applying an import requires --offline, --source-sha256 and --archive")
			}
			topology := localTopology()
			backend, path, err := persistence.ResolveLocation(flagBackend, statePath(), topology)
			if err != nil {
				return err
			}
			repo, err := persistence.OpenImportSource(cmd.Context(), backend, path)
			if err != nil {
				return err
			}
			defer repo.Close()
			var plan *persistence.Format4ImportPlan
			if format == 5 {
				if apply {
					plan, err = repo.ImportFormat5(cmd.Context(), topology, sourceSHA256, archive)
				} else {
					plan, err = repo.PlanFormat5Import(cmd.Context(), topology)
				}
			} else if apply {
				plan, err = repo.ImportFormat4(cmd.Context(), topology, sourceSHA256, archive)
			} else {
				plan, err = repo.PlanFormat4Import(cmd.Context(), topology)
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "commit the verified conversion (default: preview only)")
	cmd.Flags().BoolVar(&offline, "offline", false, "assert all old writers are stopped; this does not establish provider stop evidence")
	cmd.Flags().StringVar(&sourceSHA256, "source-sha256", "", "source digest returned by preview")
	cmd.Flags().StringVar(&archive, "archive", "", "durable archive path for exact original aggregate bytes; never overwrites different data")
	return cmd
}

func init() { stateCmd.AddCommand(newStateImportFormat4Command(), newStateImportFormat5Command()) }
