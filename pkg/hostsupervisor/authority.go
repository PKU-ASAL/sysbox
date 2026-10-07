package hostsupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"io"
	"net/http"
	"strings"
	"time"
)

// AuthorityClient authenticates read-side authorization checks as this Agent.
// Stop reports carry the separately signed host receipt, never a worker claim.
type AuthorityClient struct {
	Identity agent.Identity
	HTTP     *http.Client
}

func (c AuthorityClient) post(ctx context.Context, path string, value any, signed bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Identity.APIURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Identity.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Identity.Token)
	}
	if signed {
		if err := agent.SignRequest(req, c.Identity.ID, c.Identity.Secret, time.Now()); err != nil {
			return err
		}
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("host authority request rejected (HTTP %d)", resp.StatusCode)
	}
	return nil
}
func (c AuthorityClient) Authorize(ctx context.Context, grant controlplane.SignedExecutionGrant) error {
	return c.post(ctx, "/v1/agents/"+c.Identity.ID+"/execution-grants/authorize", grant, true)
}
func (c AuthorityClient) ReportStop(ctx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
	return c.post(ctx, "/v1/hosts/"+c.Identity.ID+"/execution-grants/stopped", receipt, false)
}
