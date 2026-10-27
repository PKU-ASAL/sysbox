package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

func newGCBatchSubmitCommand() *cobra.Command {
	var endpoint, topology, key, token string
	cmd := &cobra.Command{Use: "batch-submit <plan.json>", Short: "Admit a bounded, ordered GC batch from a complete inventory", Args: cobra.ExactArgs(1), SilenceUsage: true}
	cmd.Flags().StringVar(&endpoint, "api", "", "control plane URL")
	cmd.Flags().StringVar(&topology, "topology", "", "one canonical topology to collect")
	cmd.Flags().StringVar(&key, "key", "", "stable batch idempotency key")
	cmd.Flags().StringVar(&token, "token", "", "API token; defaults to SYSBOX_API_TOKEN")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		base, err := url.Parse(endpoint)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			return fmt.Errorf("valid --api required")
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(topology) || key == "" || len(key) > 128 {
			return fmt.Errorf("--topology and --key required")
		}
		file, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer file.Close()
		var plan gcBatchPlan
		decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("one plan object required")
		}
		now := time.Now()
		if plan.Version != 1 || !plan.Complete || plan.Scope != "local-filesystem-and-host" || plan.CreatedAt.After(now) || now.Sub(plan.CreatedAt) > 15*time.Minute {
			return fmt.Errorf("plan must be complete and at most 15 minutes old")
		}
		candidates := []controlplane.GCCandidate{}
		for _, c := range plan.Candidates {
			if plan.Topologies[c.TopologyUID] == topology {
				if err := c.Validate(); err != nil {
					return err
				}
				candidates = append(candidates, c)
			}
		}
		if len(candidates) < 1 || len(candidates) > 128 {
			return fmt.Errorf("selected topology requires 1..128 retained candidates")
		}
		raw, err := json.Marshal(struct {
			Candidates []controlplane.GCCandidate `json:"candidates"`
		}{candidates})
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/topologies/"+topology+"/gc/batch", bytes.NewReader(raw))
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
			return fmt.Errorf("batch admission rejected (HTTP %d); retain the original key and plan", response.StatusCode)
		}
		receipt, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil {
			return err
		}
		if len(receipt) > 1<<20 || !json.Valid(receipt) {
			return fmt.Errorf("invalid batch receipt")
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(receipt))
		return err
	}
	return cmd
}
func init() { gcCmd.AddCommand(newGCBatchSubmitCommand()) }
