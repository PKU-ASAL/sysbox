package commands

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/spf13/cobra"
)

var pauseCmd = newPowerCommand("pause")
var resumeCmd = newPowerCommand("resume")

func newPowerCommand(operation string) *cobra.Command {
	var service serviceOptions
	var key string
	var detach bool
	cmd := &cobra.Command{Use: operation + " <sysbox_node.name>", Short: operation + " a node through the host service", Args: cobra.ExactArgs(1), Annotations: map[string]string{"service-client": "true"}}
	service.flags(cmd, true)
	cmd.Flags().StringVar(&key, "key", "", "stable request key; reuse for the same operation")
	cmd.Flags().BoolVar(&detach, "detach", false, "return after durable acceptance")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := rejectLocalExecutionFlags(cmd); err != nil {
			return err
		}
		target, err := address.Parse(args[0])
		if err != nil {
			return err
		}
		if target.Type != "sysbox_node" || !target.Equal(address.Resource(target.Type, target.Name)) {
			return fmt.Errorf("power target must be an unindexed root sysbox_node.name")
		}
		client, err := service.client()
		if err != nil {
			return err
		}
		defer client.http.CloseIdleConnections()
		if key == "" {
			key = uuid.NewString()
		}
		if !clientKey.MatchString(key) {
			return fmt.Errorf("invalid request key")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s in topology %s\nRequest key: %s\n", operation, target, service.topology, key)
		var accepted controlplane.NodeOperation
		path := "/v1/topologies/" + service.topology + "/nodes/" + target.Name + "/" + operation
		if err = client.request(cmd.Context(), http.MethodPost, path, key, nil, &accepted); err != nil {
			return fmt.Errorf("request %s: %w", key, err)
		}
		if !clientSegment.MatchString(accepted.ID) || accepted.Topology != service.topology || accepted.Node != target.Name || accepted.Operation != operation {
			return fmt.Errorf("service returned a different node operation identity; retry the same key")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Operation: %s\n", accepted.ID)
		if detach {
			return nil
		}
		return waitPowerOperation(cmd.Context(), client, accepted, cmd)
	}
	return cmd
}

func waitPowerOperation(ctx context.Context, client *serviceClient, accepted controlplane.NodeOperation, cmd *cobra.Command) error {
	for {
		var current controlplane.NodeOperation
		if err := client.request(ctx, http.MethodGet, "/v1/node-operations/"+accepted.ID, "", nil, &current); err != nil {
			return err
		}
		if current.ID != accepted.ID || current.Topology != accepted.Topology || current.Node != accepted.Node || current.Operation != accepted.Operation || current.ResourceUID != accepted.ResourceUID || current.Generation != accepted.Generation || current.AgentID != accepted.AgentID {
			return fmt.Errorf("node operation identity changed")
		}
		switch current.Status {
		case controlplane.NodeOperationStatusDone:
			fmt.Fprintf(cmd.OutOrStdout(), "Operation %s: done\n", current.ID)
			return nil
		case controlplane.NodeOperationStatusFailed:
			return fmt.Errorf("operation %s failed; inspect its result before issuing a new key", current.ID)
		case controlplane.NodeOperationStatusQueued, controlplane.NodeOperationStatusRunning:
		default:
			return fmt.Errorf("operation %s has unrecognized status %q", current.ID, current.Status)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w; operation %s may still be running; retry the original key to query it", ctx.Err(), accepted.ID)
		case <-timer.C:
		}
	}
}
