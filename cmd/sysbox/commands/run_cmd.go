package commands

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newRunCommand() *cobra.Command {
	root := &cobra.Command{Use: "run", Short: "Query, wait for or request cancellation of a service Run"}
	for _, action := range []string{"show", "wait", "cancel"} {
		var service serviceOptions
		cmd := &cobra.Command{Annotations: map[string]string{"service-client": "true"}, Use: action + " <run-id>", Args: cobra.ExactArgs(1)}
		service.flags(cmd, false)
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			client, err := service.client()
			if err != nil {
				return err
			}
			defer client.http.CloseIdleConnections()
			id := args[0]
			if !clientSegment.MatchString(id) {
				return fmt.Errorf("invalid Run ID")
			}
			switch action {
			case "wait":
				fmt.Fprintf(cmd.OutOrStdout(), "Waiting for Run %s; Ctrl-C only stops waiting.\n", id)
				return client.wait(cmd.Context(), id, "", "", cmd.OutOrStdout())
			case "cancel":
				if err = client.request(cmd.Context(), http.MethodPost, "/v1/runs/"+id+"/cancel", "", nil, nil); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cancellation requested for Run %s; query its result to confirm termination.\n", id)
				return nil
			default:
				run, err := client.run(cmd.Context(), id)
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(run)
			}
		}
		root.AddCommand(cmd)
	}
	return root
}
