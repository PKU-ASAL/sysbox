package commands

import (
	"bytes"
	"context"
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

var clientSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var clientKey = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,128}$`)
var clientRevision = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// serviceClient is an authenticated request adapter, with no provider or state
// access. A transport error never selects a different execution implementation.
type serviceClient struct {
	endpoint, token string
	http            *http.Client
}

type serviceOptions struct{ endpoint, token, topology string }

func (o *serviceOptions) flags(cmd *cobra.Command, topology bool) {
	cmd.Flags().StringVar(&o.endpoint, "api", "", "service URL (defaults to SYSBOX_API_URL or http://127.0.0.1:9876)")
	cmd.Flags().StringVar(&o.token, "token", "", "API token (defaults to SYSBOX_API_TOKEN)")
	if topology {
		cmd.Flags().StringVar(&o.topology, "topology", "default", "canonical topology name")
	}
}

func (o serviceOptions) client() (*serviceClient, error) {
	endpoint := o.endpoint
	if endpoint == "" {
		endpoint = os.Getenv("SYSBOX_API_URL")
	}
	if endpoint == "" {
		endpoint = "http://127.0.0.1:9876"
	}
	base, err := url.Parse(endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return nil, fmt.Errorf("--api must be an HTTP(S) URL without credentials, query or fragment")
	}
	if o.topology != "" && !clientSegment.MatchString(o.topology) {
		return nil, fmt.Errorf("invalid topology name")
	}
	token := o.token
	if token == "" {
		token = os.Getenv("SYSBOX_API_TOKEN")
	}
	return &serviceClient{endpoint: strings.TrimRight(endpoint, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *serviceClient) request(ctx context.Context, method, path, key string, body, out any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	if len(raw) > 8<<20 {
		return fmt.Errorf("request exceeds the service's 8 MiB limit")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("service request failed; outcome unconfirmed (retry the same request key)")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Server diagnostics may contain user input. Do not echo arbitrary bodies.
		return fmt.Errorf("service rejected %s %s (HTTP %d)", method, path, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return fmt.Errorf("oversized service response")
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid service response")
	}
	return nil
}

type serviceRun struct {
	controlplane.Run
	InputsRequired bool `json:"inputs_required,omitempty"`
}

func (c *serviceClient) run(ctx context.Context, id string) (*serviceRun, error) {
	if !clientSegment.MatchString(id) {
		return nil, fmt.Errorf("invalid Run ID")
	}
	var run serviceRun
	if err := c.request(ctx, http.MethodGet, "/v1/runs/"+id, "", nil, &run); err != nil {
		return nil, err
	}
	if run.ID != id {
		return nil, fmt.Errorf("service returned a different Run identity")
	}
	return &run, nil
}

func (c *serviceClient) wait(ctx context.Context, id, topology, operation string, out io.Writer) error {
	last := controlplane.RunStatus("")
	for {
		run, err := c.run(ctx, id)
		if err != nil {
			return err
		}
		if topology != "" && run.Topology != topology || operation != "" && run.Op != operation {
			return fmt.Errorf("Run scope does not match submitted operation")
		}
		if run.Status != last {
			fmt.Fprintf(out, "Run %s: %s\n", id, run.Status)
			last = run.Status
		}
		if run.InputsRequired {
			return fmt.Errorf("Run %s requires resubmission with the original key and inputs", id)
		}
		switch run.Status {
		case controlplane.RunDone:
			return nil
		case controlplane.RunFailed, controlplane.RunCancelled:
			return fmt.Errorf("Run %s ended with status %s; inspect its result before retrying", id, run.Status)
		case controlplane.RunQueued, controlplane.RunAssigned, controlplane.RunRunning:
		default:
			return fmt.Errorf("Run %s has unrecognized status %q", id, run.Status)
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
