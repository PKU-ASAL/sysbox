package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/spf13/cobra"
)

func newImportInspectCommand() *cobra.Command { return newRemoteImportCommand(false) }
func newImportConfirmCommand() *cobra.Command { return newRemoteImportCommand(true) }
func newRemoteImportCommand(confirm bool) *cobra.Command {
	var api, token, key, topology, provider string
	command := &cobra.Command{Use: "inspect <resource_type.name> <external_id>", Short: "Request read-only adoption inspection from the owning Agent", Args: cobra.ExactArgs(2)}
	if confirm {
		command.Use = "confirm <inspection_id>"
		command.Short = "Confirm adoption using persisted host inspection evidence"
		command.Args = cobra.ExactArgs(1)
	}
	command.Flags().StringVar(&api, "api", "", "control plane API URL")
	command.Flags().StringVar(&token, "token", "", "API token (defaults to SYSBOX_API_TOKEN)")
	command.Flags().StringVar(&key, "key", "", "stable idempotency key")
	command.Flags().StringVar(&topology, "topology", "", "canonical topology name")
	if !confirm {
		command.Flags().StringVar(&provider, "substrate", "", "provider to inspect")
	}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		base, err := url.Parse(api)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			return fmt.Errorf("valid --api URL required")
		}
		valid := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
		if !valid.MatchString(topology) || key == "" || len(key) > 128 {
			return fmt.Errorf("valid --topology and --key required")
		}
		path := "/v1/topologies/" + topology + "/adoption-inspections"
		var body []byte
		if confirm {
			if !valid.MatchString(args[0]) {
				return fmt.Errorf("invalid inspection ID")
			}
			path += "/" + args[0] + "/confirm"
		} else {
			parsed, err := address.Parse(args[0])
			if err != nil || parsed.Type != "sysbox_node" || parsed.String() != "sysbox_node."+parsed.Name || provider == "" {
				return fmt.Errorf("node address and --substrate required")
			}
			body, err = json.Marshal(map[string]string{"type": parsed.Type, "name": parsed.Name, "id": args[1], "substrate": provider})
			if err != nil {
				return err
			}
		}
		request, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, strings.TrimRight(api, "/")+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
		if token == "" {
			token = os.Getenv("SYSBOX_API_TOKEN")
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 202 {
			return fmt.Errorf("adoption request rejected (HTTP %d)", response.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return fmt.Errorf("invalid operation response")
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
		return err
	}
	return command
}
func init() { importCmd.AddCommand(newImportInspectCommand(), newImportConfirmCommand()) }
