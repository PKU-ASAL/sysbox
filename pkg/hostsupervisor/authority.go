package hostsupervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthorityClient authenticates read-side authorization checks as this Agent.
// Stop reports carry the separately signed host receipt, never a worker claim.
type AuthorityClient struct {
	Identity agent.Identity
	HTTP     *http.Client
}

// IssueCommandGrant uses authenticated command admission, including current
// agent policy and source revalidation. It does not accept a replacement payload.
func (c AuthorityClient) IssueCommandGrant(ctx context.Context, command controlplane.AgentCommand, sessionID, bootID string) (controlplane.SignedExecutionGrant, error) {
	var signed controlplane.SignedExecutionGrant
	if command.AgentID != c.Identity.ID || command.ID == "" {
		return signed, controlplane.ErrInvalidExecutionGrant
	}
	err := c.postJSON(ctx, "/v1/agents/"+url.PathEscape(c.Identity.ID)+"/commands/"+url.PathEscape(command.ID)+"/execution-grant", map[string]any{"session_id": sessionID, "lease_owner": command.LeaseOwner, "attempt": command.Attempt, "boot_id": bootID}, &signed)
	return signed, err
}

func (c AuthorityClient) post(ctx context.Context, path string, value any, signed bool) error {
	return c.postJSON(ctx, path, value, nil, signed)
}

func (c AuthorityClient) postJSON(ctx context.Context, path string, value any, out any, signed ...bool) error {
	return c.postJSONLimit(ctx, path, value, out, 1<<20, signed...)
}

func (c AuthorityClient) postJSONLimit(ctx context.Context, path string, value any, out any, responseLimit int64, signed ...bool) error {
	authenticated := len(signed) == 0 || signed[0]
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
	if authenticated {
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
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, responseLimit)).Decode(out); err != nil {
			return err
		}
	}
	return nil
}
func (c AuthorityClient) Authorize(ctx context.Context, grant controlplane.SignedExecutionGrant) error {
	// Authorization is a read-side check of this exact existing grant. Retry
	// connection failures only, without extending the caller's watchdog budget
	// or replaying issue/renew/stop mutations. Any HTTP rejection is final.
	for attempt := 0; ; attempt++ {
		err := c.post(ctx, "/v1/agents/"+c.Identity.ID+"/execution-grants/authorize", grant, true)
		var transport *url.Error
		if err == nil || attempt == 2 || ctx.Err() != nil || !errors.As(err, &transport) {
			return err
		}
		delay := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			delay.Stop()
			return ctx.Err()
		case <-delay.C:
		}
	}
}
func (c AuthorityClient) ReportStop(ctx context.Context, receipt controlplane.SignedExecutionStopReceipt) error {
	return c.post(ctx, "/v1/hosts/"+c.Identity.ID+"/execution-grants/stopped", receipt, false)
}

func (c AuthorityClient) PendingGuestTaskStops(ctx context.Context, cursor controlplane.GuestTaskStopCursor) (controlplane.GuestTaskStopPage, error) {
	var page controlplane.GuestTaskStopPage
	err := c.postJSON(ctx, "/v1/agents/"+c.Identity.ID+"/guest-task-stops", cursor, &page)
	return page, err
}

func (c AuthorityClient) ReportRun(ctx context.Context, runID string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
	if runID == "" || strings.ContainsAny(runID, "/\\?#\x00") {
		return fmt.Errorf("invalid run ID")
	}
	return c.post(ctx, "/v1/hosts/"+c.Identity.ID+"/runs/"+runID+"/completed", map[string]any{"receipt": receipt, "evidence": evidence}, false)
}

func (c AuthorityClient) DeriveGuestGrant(ctx context.Context, parent controlplane.SignedExecutionGrant, taskID, taskDigest, resourceUID string, generation int64, guestBootID string) (controlplane.SignedExecutionGrant, error) {
	var out controlplane.SignedExecutionGrant
	err := c.postJSON(ctx, "/v1/agents/"+c.Identity.ID+"/execution-grant/derive-guest", map[string]any{"parent": parent, "task_id": taskID, "task_digest": taskDigest, "resource_uid": resourceUID, "generation": generation, "guest_boot_id": guestBootID}, &out)
	return out, err
}

func (c AuthorityClient) RenewDerivedGuestGrant(ctx context.Context, parent, child controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
	var out controlplane.SignedExecutionGrant
	err := c.postJSON(ctx, "/v1/agents/"+c.Identity.ID+"/execution-grant/renew-guest", map[string]any{"parent": parent, "child": child}, &out)
	return out, err
}

func (c AuthorityClient) DeriveSSHGrant(ctx context.Context, hostKey ed25519.PrivateKey, parent controlplane.SignedExecutionGrant, request substrate.SSHAccessMutation, resourceUID string, generation int64, bootID string) (controlplane.SignedExecutionGrant, error) {
	var out controlplane.SignedExecutionGrant
	proof, err := controlplane.SignSSHGrantDerivation(hostKey, controlplane.SSHGrantDerivation{Parent: parent, Request: request, ResourceUID: resourceUID, Generation: generation, GuestBootID: bootID})
	if err != nil {
		return out, err
	}
	err = c.postJSON(ctx, "/v1/agents/"+c.Identity.ID+"/execution-grant/derive-ssh", proof, &out)
	return out, err
}

func (c AuthorityClient) ReportDerivedSSHAccess(ctx context.Context, request substrate.SSHAccessMutation, receipt controlplane.SignedExecutionStopReceipt) error {
	if request.ID == "" || strings.ContainsAny(request.ID, "/\\?#\x00") {
		return fmt.Errorf("invalid SSH action ID")
	}
	return c.post(ctx, "/v1/hosts/"+c.Identity.ID+"/derived-ssh/"+request.ID+"/completed", map[string]any{"request": request, "receipt": receipt}, false)
}

func (c AuthorityClient) RequestGCGrant(ctx context.Context, candidate GCCandidate, topology, sessionID, actionID, commandID, leaseOwner string, attempt int, bootID string) (controlplane.SignedExecutionGrant, error) {
	var out controlplane.SignedExecutionGrant
	err := c.postJSON(ctx, "/v1/agents/"+c.Identity.ID+"/gc-grant", map[string]any{"candidate": candidate, "topology": topology, "session_id": sessionID, "action_id": actionID, "command_id": commandID, "attempt": attempt, "lease_owner": leaseOwner, "boot_id": bootID}, &out)
	return out, err
}

func (c AuthorityClient) ReportNodeOperation(ctx context.Context, operation string, receipt controlplane.SignedExecutionStopReceipt, evidence json.RawMessage) error {
	return c.post(ctx, "/v1/hosts/"+c.Identity.ID+"/node-operations/"+operation+"/completed", map[string]any{"receipt": receipt, "evidence": evidence}, false)
}

func (c AuthorityClient) FetchFile(ctx context.Context, id string) (io.ReadCloser, error) {
	// IDs are validated by the canonical installation contract before entry.
	if id == "" || strings.ContainsAny(id, "/\\?#\x00") {
		return nil, fmt.Errorf("invalid installation ID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Identity.APIURL, "/")+"/v1/agents/"+c.Identity.ID+"/guest-files/"+id, nil)
	if err != nil {
		return nil, err
	}
	if c.Identity.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Identity.Token)
	}
	if err = agent.SignRequest(req, c.Identity.ID, c.Identity.Secret, time.Now()); err != nil {
		return nil, err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("file payload fetch rejected (HTTP %d)", response.StatusCode)
	}
	return response.Body, nil
}
