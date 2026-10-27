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

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/spf13/cobra"
)

// Recovery uses the configured API/proxy authentication policy. In particular,
// the CLI must not invent an administrator subject or role header.
func newHostFenceRemoteCommand(operation string) *cobra.Command {
	var endpoint, token, topology, observation, key, decisionPath string
	cmd := &cobra.Command{Use: operation + " <fence-id>", Args: cobra.ExactArgs(1)}
	cmd.Flags().StringVar(&endpoint, "api", "", "control plane or authenticated proxy URL")
	cmd.Flags().StringVar(&token, "token", "", "API token (defaults to SYSBOX_API_TOKEN)")
	cmd.Flags().StringVar(&topology, "topology", "", "canonical topology name")
	switch operation {
	case "recovery-plan":
		cmd.Short = "Read intent recovery choices and blockers without changing state"
		cmd.Flags().StringVar(&observation, "observation", "", "owning-agent observation command ID")
	case "observe":
		cmd.Short = "Request an owning-agent observation after admitted host fencing"
		cmd.Flags().StringVar(&key, "key", "", "stable idempotency key for this observation")
	case "reconcile-intents":
		cmd.Short = "Submit an explicit evidence-bound disposition without replaying old effects"
		cmd.Flags().StringVar(&decisionPath, "decision", "", "JSON containing decision and observation_command_id")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		base, err := url.Parse(endpoint)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
			return fmt.Errorf("--api must be an HTTP(S) URL without credentials, query or fragment")
		}
		valid := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
		if !valid.MatchString(topology) || !valid.MatchString(args[0]) {
			return fmt.Errorf("valid --topology and fence ID required")
		}
		path := "/v1/topologies/" + topology + "/host-fences/" + args[0] + "/" + operation
		method, want := http.MethodPost, http.StatusOK
		var body []byte
		switch operation {
		case "recovery-plan":
			method = http.MethodGet
			if observation != "" {
				if !valid.MatchString(observation) {
					return fmt.Errorf("invalid observation ID")
				}
				path += "?observation=" + url.QueryEscape(observation)
			}
		case "observe":
			if strings.TrimSpace(key) == "" || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
				return fmt.Errorf("--key requires 1-128 bytes without line breaks")
			}
			want = http.StatusAccepted
		case "reconcile-intents":
			var input struct {
				Decision    controlplane.IntentDispositionDecision `json:"decision"`
				Observation string                                 `json:"observation_command_id"`
			}
			if err := readIntentDecision(decisionPath, &input); err != nil {
				return err
			}
			if _, err := input.Decision.Digest(); err != nil {
				return err
			}
			if input.Decision.Topology != topology || input.Decision.FenceID != args[0] || !valid.MatchString(input.Observation) {
				return fmt.Errorf("decision topology, fence or observation does not match request")
			}
			key = input.Decision.ID
			body, err = json.Marshal(input)
			if err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(cmd.Context(), method, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
		}
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
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			return fmt.Errorf("host-fence %s rejected (HTTP %d); verify API authorization and evidence; retry the unchanged decision/key after an uncertain response", operation, response.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		if err != nil {
			return err
		}
		if len(raw) > 4<<20 || !json.Valid(raw) {
			return fmt.Errorf("invalid or oversized host-fence response")
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
		return err
	}
	return cmd
}

func readIntentDecision(path string, target any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("decision must be a regular JSON file at most 1MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("decision must be a regular JSON file at most 1MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return fmt.Errorf("decision too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("one decision JSON object required")
	}
	return nil
}
