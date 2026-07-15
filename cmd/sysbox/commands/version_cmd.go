package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/buildinfo"
)

func newVersionCmd() *cobra.Command {
	var outputJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print Sysbox build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := buildinfo.Current()
			if outputJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "sysbox %s\ncommit: %s\nbuild time: %s\ngo: %s\n", info.Version, info.Commit, info.BuildTime, info.GoVersion)
			return err
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "print build information as JSON")
	return cmd
}

var versionCmd = newVersionCmd()
