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

func newGCSubmitCommand() *cobra.Command {
	var endpoint, token, key string
	cmd := &cobra.Command{Use: "submit <plan.json>", Short: "Submit one GC candidate for deletion by its owning Agent", Args: cobra.ExactArgs(1)}
	cmd.Flags().StringVar(&endpoint, "api", "", "control plane API URL")
	cmd.Flags().StringVar(&token, "token", "", "API token (defaults to SYSBOX_API_TOKEN)")
	cmd.Flags().StringVar(&key, "key", "", "stable idempotency key for this deletion")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		base, err := url.Parse(endpoint)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
			return fmt.Errorf("--api must be an HTTP(S) URL without credentials or query")
		}
		if key == "" || len(key) > 128 {
			return fmt.Errorf("--key requires 1-128 bytes")
		}
		file, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer file.Close()
		var plan struct {
			Topology  string                   `json:"topology"`
			Candidate controlplane.GCCandidate `json:"candidate"`
		}
		decoder := json.NewDecoder(io.LimitReader(file, 16385))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return fmt.Errorf("invalid GC plan: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("GC plan must contain one JSON object")
		}
		if plan.Topology == "" || strings.ContainsAny(plan.Topology, "/\\?#\x00") || plan.Topology == "." || plan.Topology == ".." {
			return fmt.Errorf("invalid plan topology")
		}
		if err := plan.Candidate.Validate(); err != nil {
			return err
		}
		raw, err := json.Marshal(plan.Candidate)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/topologies/"+url.PathEscape(plan.Topology)+"/gc", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		if token == "" {
			token = os.Getenv("SYSBOX_API_TOKEN")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		client := http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			return fmt.Errorf("GC admission rejected (HTTP %d); inspect the plan and operation before retrying with the same key", response.StatusCode)
		}
		raw, err = io.ReadAll(io.LimitReader(response.Body, 16385))
		if err != nil {
			return err
		}
		if len(raw) > 16384 || !json.Valid(raw) {
			return fmt.Errorf("invalid GC admission receipt")
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
		return err
	}
	return cmd
}

func init() { gcCmd.AddCommand(newGCSubmitCommand()) }
