package agentexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/oslab/sysbox/pkg/buildinfo"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
)

func checkAPIBuild(ctx context.Context, opts Options, local buildinfo.Info) error {
	endpoint, err := url.Parse(opts.APIURL)
	if err != nil || endpoint == nil || endpoint.Host == "" || endpoint.User != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return fmt.Errorf("invalid API URL for build check")
	}
	endpoint.Path = endpoint.Path + "/v1/health"
	endpoint.RawQuery, endpoint.Fragment = "", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	authorize(req, opts)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("API build check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API build check: HTTP %d", resp.StatusCode)
	}
	var report struct {
		Build    buildinfo.Info `json:"build"`
		Protocol string         `json:"agent_protocol"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&report); err != nil {
		return fmt.Errorf("API build response: %w", err)
	}
	if report.Protocol != controlplane.AgentProtocolVersion {
		return fmt.Errorf("API agent protocol mismatch: %q", report.Protocol)
	}
	if err := buildinfo.CheckCompatible(local, report.Build); err != nil {
		return fmt.Errorf("API: %w", err)
	}
	return nil
}

func checkComponentBuilds(ctx context.Context, opts Options) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	local := buildinfo.Current()
	host, err := hostsupervisor.NewTaskClient(opts.HostSupervisorSocket, hostsupervisor.TargetRef{}).BuildInfo(ctx)
	if err != nil {
		return fmt.Errorf("host build check: %w", err)
	}
	if err := buildinfo.CheckCompatible(local, host); err != nil {
		return fmt.Errorf("host: %w", err)
	}
	if err := checkAPIBuild(ctx, opts, local); err != nil {
		return err
	}
	if local.Unstamped() {
		fmt.Println("[agent] unstamped development builds: release identity is not verified")
	}
	return nil
}
