package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/spf13/cobra"
)

// Each step is explicit: preparing is not permission to retire the source.
// The enrolled host produces the signed proof via host-fence prepare-transfer.
func newOwnershipTransferCommand() *cobra.Command {
	root := &cobra.Command{Use: "ownership-transfer", Short: "Prepare and submit an explicit ownership transfer through the API"}
	for _, operation := range []string{"prepare", "prepared", "submit", "abort"} {
		var endpoint, token string
		command := &cobra.Command{Use: operation + " <plan-or-proof.json>", Args: cobra.ExactArgs(1)}
		command.Flags().StringVar(&endpoint, "api", "", "control plane or authenticated proxy URL")
		command.Flags().StringVar(&token, "token", "", "API token (defaults to SYSBOX_API_TOKEN)")
		command.RunE = func(cmd *cobra.Command, args []string) error {
			base, err := url.Parse(endpoint)
			if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
				return fmt.Errorf("--api must be an HTTP(S) URL without credentials, query or fragment")
			}
			var plan controlplane.OwnershipTransferPlan
			var body any
			if operation == "prepared" {
				var proof controlplane.OwnershipPreparation
				if err := readIntentDecision(args[0], &proof); err != nil {
					return err
				}
				plan = proof.Plan
				// Authenticity and canonical epoch/boot verification belong to the API.
				if proof.SourceEpoch == 0 || proof.HostBootID == "" || proof.ObservedAt.IsZero() || len(proof.Signature) != 64 {
					return fmt.Errorf("signed host preparation proof required")
				}
				body = proof
			} else {
				if err := readIntentDecision(args[0], &plan); err != nil {
					return err
				}
				body = plan
			}
			if err := plan.Validate(); err != nil {
				return err
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			path := "/v1/topologies/" + plan.TargetTopology + "/ownership-transfers"
			if operation != "submit" {
				path += "/" + operation
			}
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(raw))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			credential := token
			if credential == "" {
				credential = os.Getenv("SYSBOX_API_TOKEN")
			}
			if credential != "" {
				req.Header.Set("Authorization", "Bearer "+credential)
			}
			client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("ownership-transfer %s response unavailable; retry the unchanged plan/proof: %w", operation, err)
			}
			defer response.Body.Close()
			accepted := response.StatusCode == http.StatusAccepted
			if operation == "prepared" {
				accepted = response.StatusCode == http.StatusOK
			}
			if operation == "abort" {
				accepted = accepted || response.StatusCode == http.StatusOK
			}
			if !accepted {
				return fmt.Errorf("ownership-transfer %s rejected (HTTP %d); verify authorization and evidence, retry only the unchanged plan/proof", operation, response.StatusCode)
			}
			output, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
			if err != nil {
				return err
			}
			if len(output) > 4<<20 || !json.Valid(output) {
				return fmt.Errorf("invalid or oversized ownership-transfer response")
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(output))
			return err
		}
		root.AddCommand(command)
	}
	return root
}
