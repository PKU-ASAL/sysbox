package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const DefaultWorkerID = "local"

type Options struct {
	APIURL       string
	ID           string
	Name         string
	Capabilities []string
	Labels       map[string]string
	Version      string
	PollInterval time.Duration
}

func Run(ctx context.Context, opts Options, bridge Bridge) error {
	if opts.APIURL == "" {
		return fmt.Errorf("api url is required")
	}
	if bridge == nil {
		return fmt.Errorf("worker bridge is required")
	}
	if opts.ID == "" {
		opts.ID = DefaultWorkerID
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if len(opts.Capabilities) == 0 {
		opts.Capabilities = []string{"docker", "network", "firecracker", "kvm", "libvirt"}
	}
	if err := post(ctx, opts.APIURL+"/v1/workers", controlplane.Worker{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil); err != nil {
		return err
	}

	executor := NewExecutorWithBridge(bridge)
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		if err := heartbeat(ctx, opts); err != nil {
			fmt.Printf("[worker] heartbeat failed: %v\n", err)
		}
		if err := pollAndExecute(ctx, executor, opts); err != nil {
			fmt.Printf("[worker] poll failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func heartbeat(ctx context.Context, opts Options) error {
	return post(ctx, opts.APIURL+"/v1/workers/"+opts.ID+"/heartbeat", controlplane.Worker{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil)
}

func pollAndExecute(ctx context.Context, executor *Executor, opts Options) error {
	var listed struct {
		Runs []controlplane.Run `json:"runs"`
	}
	if err := get(ctx, opts.APIURL+"/v1/workers/"+opts.ID+"/runs", &listed); err != nil {
		return err
	}
	for _, run := range listed.Runs {
		claimed, err := claim(ctx, opts, run.ID)
		if err != nil {
			fmt.Printf("[worker] claim %s failed: %v\n", run.ID, err)
			continue
		}
		executor.Execute(claimed)
	}
	return nil
}

func claim(ctx context.Context, opts Options, runID string) (*controlplane.Run, error) {
	var run controlplane.Run
	if err := post(ctx, opts.APIURL+"/v1/workers/"+opts.ID+"/runs/"+runID+"/claim", nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func get(ctx context.Context, url string, out any) error {
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

func post(ctx context.Context, url string, in any, out any) error {
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
