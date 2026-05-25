package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

type WorkerOptions struct {
	APIURL       string
	ID           string
	Name         string
	Capabilities []string
	Labels       map[string]string
	Version      string
	PollInterval time.Duration
}

func RunWorker(ctx context.Context, cfg config.ServiceConfig, opts WorkerOptions) error {
	if opts.APIURL == "" {
		return fmt.Errorf("api url is required")
	}
	if opts.ID == "" {
		opts.ID = DefaultWorkerID
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if len(opts.Capabilities) == 0 {
		opts.Capabilities = localWorker().Capabilities
	}
	if err := workerPost(ctx, opts.APIURL+"/v1/workers", controlplane.Worker{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil); err != nil {
		return err
	}

	execServer := NewServerWithConfig(cfg)
	execServer.jobs = newJobsWithRecovery(execServer.runsDir, execServer.apiStore, false)
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		if err := heartbeatWorker(ctx, opts); err != nil {
			fmt.Printf("[worker] heartbeat failed: %v\n", err)
		}
		if err := pollAndExecuteWorkerRun(ctx, execServer, opts); err != nil {
			fmt.Printf("[worker] poll failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func heartbeatWorker(ctx context.Context, opts WorkerOptions) error {
	return workerPost(ctx, opts.APIURL+"/v1/workers/"+opts.ID+"/heartbeat", controlplane.Worker{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil)
}

func pollAndExecuteWorkerRun(ctx context.Context, execServer *Server, opts WorkerOptions) error {
	var listed struct {
		Runs []Run `json:"runs"`
	}
	if err := workerGet(ctx, opts.APIURL+"/v1/workers/"+opts.ID+"/runs", &listed); err != nil {
		return err
	}
	for _, run := range listed.Runs {
		claimed, err := claimWorkerRun(ctx, opts, run.ID)
		if err != nil {
			fmt.Printf("[worker] claim %s failed: %v\n", run.ID, err)
			continue
		}
		execServer.executeRunLocally(claimed)
	}
	return nil
}

func claimWorkerRun(ctx context.Context, opts WorkerOptions, runID string) (*Run, error) {
	var run Run
	if err := workerPost(ctx, opts.APIURL+"/v1/workers/"+opts.ID+"/runs/"+runID+"/claim", nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Server) executeRunLocally(run *Run) {
	if run == nil {
		return
	}
	run.logs = &Broadcaster{}
	s.jobs.mu.Lock()
	s.jobs.runs[run.ID] = run
	s.jobs.mu.Unlock()
	switch run.Op {
	case "apply":
		if run.ParentID != "" {
			if parent, err := s.apiStore.GetRun(context.Background(), run.ParentID); err == nil {
				s.runResumeApply(parent, run)
				return
			}
		}
		s.runApply(run.Topology, run)
	case "destroy":
		if run.ParentID != "" {
			if parent, err := s.apiStore.GetRun(context.Background(), run.ParentID); err == nil {
				s.runResumeDestroy(parent, run)
				return
			}
		}
		s.runDestroy(run.Topology, run)
	default:
		s.jobs.finish(run, fmt.Errorf("unsupported run op %q", run.Op))
	}
}

func workerGet(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func workerPost(ctx context.Context, url string, in any, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %s", url, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
