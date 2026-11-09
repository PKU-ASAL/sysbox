package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/spf13/cobra"
)

func rejectLocalExecutionFlags(cmd *cobra.Command) error {
	for _, name := range []string{"state", "backend", "allow-unsafe-state"} {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--%s is not supported by service operations; configure storage on the service and select --topology", name)
		}
	}
	return nil
}

func newLifecycleCommand(operation string) *cobra.Command {
	var service serviceOptions
	var file, revision, key, target, resume, inputsFile, deadline string
	var includes []string
	var approve, detach bool
	command := &cobra.Command{Annotations: map[string]string{"service-client": "true"}, Use: operation, Short: strings.Title(operation) + " resources through the host service", Args: cobra.NoArgs}
	service.flags(command, true)
	command.Flags().StringVar(&key, "key", "", "stable operation key; reuse only for an unchanged request")
	command.Flags().BoolVar(&approve, "auto-approve", false, "confirm submission without prompting")
	command.Flags().BoolVar(&detach, "detach", false, "return after durable acceptance")
	if operation != "destroy" {
		command.Flags().StringVarP(&file, "file", "f", "field.sysbox.hcl", "root HCL configuration to publish")
		command.Flags().StringVar(&revision, "revision", "", "use an already published immutable revision")
		command.Flags().StringArrayVar(&includes, "include", nil, "additional project file or directory, relative to the HCL directory")
		command.Flags().StringVar(&target, "target", "", "select one resource (Reset requires sysbox_node.name)")
	}
	if operation == "apply" || operation == "plan" || operation == "reset" {
		command.Flags().StringVar(&inputsFile, "inputs", "", "JSON input values; sent transiently, never included in the revision")
		if operation != "reset" {
			command.Flags().StringVar(&deadline, "deadline", "", "absolute execution deadline (RFC3339)")
		}
	}
	if operation == "reset" {
		command.Use = "reset [--target sysbox_node.name] [--resume <run-id>]"
		command.Flags().StringVar(&resume, "resume", "", "continue the persisted reset Run and its original complete scope")
	}
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := rejectLocalExecutionFlags(cmd); err != nil {
			return err
		}
		client, err := service.client()
		if err != nil {
			return err
		}
		defer client.http.CloseIdleConnections()
		if target != "" {
			a, err := address.Parse(target)
			if err != nil {
				return err
			}
			if !a.Equal(address.Resource(a.Type, a.Name)) {
				return fmt.Errorf("target requires an unindexed root resource address")
			}
			if operation == "reset" && a.Type != "sysbox_node" {
				return fmt.Errorf("reset target must be sysbox_node.name")
			}
		}
		if key == "" {
			key = uuid.NewString()
		}
		if !clientKey.MatchString(key) {
			return fmt.Errorf("invalid operation key")
		}
		var body any
		path := "/v1/topologies/" + service.topology + "/" + operation
		if resume != "" {
			if !clientSegment.MatchString(resume) {
				return fmt.Errorf("invalid parent Run ID")
			}
			if target != "" || revision != "" || len(includes) > 0 || cmd.Flags().Changed("file") {
				return fmt.Errorf("--resume retains the parent's revision and scope; do not combine it with --target, --revision, --file or --include")
			}
			parent, err := client.run(cmd.Context(), resume)
			if err != nil {
				return err
			}
			if parent.Op != "reset" || parent.Topology != service.topology {
				return fmt.Errorf("parent Run is not a reset in the selected topology")
			}
			path = "/v1/runs/" + resume + "/resume"
			inputs, err := readLifecycleInputs(inputsFile)
			if err != nil {
				return err
			}
			if inputs != nil {
				body = map[string]any{"inputs": inputs}
			}
		} else if operation != "destroy" {
			if revision != "" && (cmd.Flags().Changed("file") || len(includes) > 0) {
				return fmt.Errorf("choose --revision or local configuration files")
			}
			if revision == "" {
				files, err := readRevisionFiles(file, includes, inputsFile)
				if err != nil {
					return err
				}
				bytes := map[string][]byte{}
				for name, raw := range files {
					bytes[name] = []byte(raw)
				}
				expected := controlplane.ComputeRevisionDigest(bytes)
				var result struct {
					Revision string `json:"revision"`
				}
				if err = client.request(cmd.Context(), http.MethodPost, "/v1/revisions", "", map[string]any{"files": files}, &result); err != nil {
					return err
				}
				if result.Revision != expected {
					return fmt.Errorf("published revision digest does not match the selected files")
				}
				revision = expected
			}
			if !clientRevision.MatchString(revision) {
				return fmt.Errorf("--revision must be a sha256 digest")
			}
			inputs, err := readLifecycleInputs(inputsFile)
			if err != nil {
				return err
			}
			request := map[string]any{"revision": revision}
			if target != "" {
				request["target"] = target
			}
			if inputs != nil {
				request["inputs"] = inputs
			}
			if deadline != "" {
				stamp, err := time.Parse(time.RFC3339, deadline)
				if err != nil {
					return fmt.Errorf("--deadline must be RFC3339")
				}
				request["deadline_at"] = stamp
			}
			body = request
		}
		if operation == "plan" {
			var preview json.RawMessage
			if err = client.request(cmd.Context(), http.MethodPost, path, "", body, &preview); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(preview)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s topology %s", strings.Title(operation), service.topology)
		if revision != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " revision %s", revision)
		}
		if target != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " target %s", target)
		}
		if resume != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " continuing %s", resume)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nRequest key: %s\n", key)
		if !approve {
			accepted, err := confirmPrompt("Submit " + operation)
			if err != nil {
				return err
			}
			if !accepted {
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
				return nil
			}
		}
		var accepted struct {
			RunID string `json:"run_id"`
		}
		if err = client.request(cmd.Context(), http.MethodPost, path, key, body, &accepted); err != nil {
			return fmt.Errorf("%s request %s: %w", operation, key, err)
		}
		if !clientSegment.MatchString(accepted.RunID) {
			return fmt.Errorf("service did not return a valid Run ID; retry request key %s", key)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\n", accepted.RunID)
		if detach {
			return nil
		}
		if err = client.wait(cmd.Context(), accepted.RunID, service.topology, operation, cmd.OutOrStdout()); err != nil {
			return fmt.Errorf("%w; query sysbox run show %s (ending this client does not cancel the Run)", err, accepted.RunID)
		}
		return nil
	}
	return command
}
