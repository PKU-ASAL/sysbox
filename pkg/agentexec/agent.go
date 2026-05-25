package agentexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/controlplane"
)

const DefaultAgentID = "local"

type Options struct {
	APIURL       string
	Token        string
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
		return fmt.Errorf("agent bridge is required")
	}
	if opts.ID == "" {
		opts.ID = DefaultAgentID
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if len(opts.Capabilities) == 0 {
		opts.Capabilities = []string{"docker", "network", "firecracker", "kvm", "libvirt"}
	}
	if err := post(ctx, opts, opts.APIURL+"/v1/agents", controlplane.Agent{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil); err != nil {
		return err
	}

	executor := NewExecutorWithBridge(remoteBridge{Bridge: bridge, reporter: opts})
	go observeLoop(ctx, opts, bridge)
	for {
		if err := heartbeat(ctx, opts); err != nil {
			fmt.Printf("[agent] heartbeat failed: %v\n", err)
		}
		if err := drainAssignedRuns(ctx, executor, opts); err != nil {
			fmt.Printf("[agent] drain assigned runs failed: %v\n", err)
		}
		if err := streamAndExecute(ctx, executor, opts); err != nil && ctx.Err() == nil {
			fmt.Printf("[agent] command stream disconnected: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}
}

func observeLoop(ctx context.Context, opts Options, bridge Bridge) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	reportObserved(ctx, opts, bridge)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reportObserved(ctx, opts, bridge)
		}
	}
}

func reportObserved(ctx context.Context, opts Options, bridge Bridge) {
	projections := Observe(ctx, opts.ID, bridge)
	for _, proj := range projections {
		if err := opts.ReportResourceProjection(ctx, proj); err != nil {
			fmt.Printf("[agent] report projection %s failed: %v\n", proj.Topology, err)
		}
	}
}

func heartbeat(ctx context.Context, opts Options) error {
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/heartbeat", controlplane.Agent{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil)
}

func drainAssignedRuns(ctx context.Context, executor *Executor, opts Options) error {
	var listed struct {
		Runs []controlplane.Run `json:"runs"`
	}
	if err := get(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs", &listed); err != nil {
		return err
	}
	for _, run := range listed.Runs {
		claimed, err := claim(ctx, opts, run.ID)
		if err != nil {
			fmt.Printf("[agent] claim %s failed: %v\n", run.ID, err)
			continue
		}
		executor.Execute(claimed)
	}
	return nil
}

func streamAndExecute(ctx context.Context, executor *Executor, opts Options) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.APIURL+"/v1/agents/"+opts.ID+"/stream", nil)
	if err != nil {
		return err
	}
	authorize(req, opts)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %s", req.URL.String(), resp.Status)
	}
	return readCommandStream(ctx, resp.Body, executor, opts)
}

func readCommandStream(ctx context.Context, body io.Reader, executor *Executor, opts Options) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "" {
			continue
		}
		var cmd command
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			fmt.Printf("[agent] decode command: %v\n", err)
			continue
		}
		switch cmd.Type {
		case "run_assigned":
			if cmd.Run == nil {
				continue
			}
			claimed, err := claim(ctx, opts, cmd.Run.ID)
			if err != nil {
				fmt.Printf("[agent] claim %s failed: %v\n", cmd.Run.ID, err)
				continue
			}
			executor.Execute(claimed)
		case "session_open":
			if cmd.Session == nil {
				continue
			}
			go func(sess controlplane.ConsoleSession, req controlplane.ConsoleRequest) {
				if err := openConsoleSession(ctx, opts, executor.bridge, sess, req); err != nil {
					fmt.Printf("[agent] console session %s failed: %v\n", sess.ID, err)
				}
			}(*cmd.Session, cmd.Request)
		case "node_operation":
			go func(op controlplane.NodeOperation) {
				completed := executor.ExecuteNodeOperation(ctx, op)
				if err := opts.ReportNodeOperationComplete(ctx, completed); err != nil {
					fmt.Printf("[agent] report node operation %s failed: %v\n", completed.ID, err)
				}
			}(cmd.Operation)
		default:
			fmt.Printf("[agent] unknown command type %q\n", cmd.Type)
		}
	}
	return scanner.Err()
}

type command struct {
	Type      string                       `json:"type"`
	Run       *controlplane.Run            `json:"run,omitempty"`
	Session   *controlplane.ConsoleSession `json:"session,omitempty"`
	Request   controlplane.ConsoleRequest  `json:"request,omitempty"`
	Operation controlplane.NodeOperation   `json:"operation,omitempty"`
}

func openConsoleSession(ctx context.Context, opts Options, bridge Bridge, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest) error {
	wsURL := strings.TrimRight(opts.APIURL, "/") + "/v1/agents/" + opts.ID + "/sessions/" + sess.ID + "/attach"
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	headers := http.Header{}
	if opts.Token != "" {
		headers.Set("Authorization", "Bearer "+opts.Token)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	return bridge.OpenConsole(ctx, sess, req, conn)
}

type remoteBridge struct {
	Bridge
	reporter Options
}

func (b remoteBridge) ReportRunComplete(ctx context.Context, run *controlplane.Run, projection controlplane.Projection) error {
	return b.reporter.ReportRunComplete(ctx, run, projection)
}

func (opts Options) ReportRunComplete(ctx context.Context, run *controlplane.Run, projection controlplane.Projection) error {
	if run == nil || opts.APIURL == "" || opts.ID == "" {
		return nil
	}
	if projection.AgentID == "" {
		projection.AgentID = opts.ID
	}
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+run.ID+"/complete", controlplane.RunCompletion{
		Run:        *run,
		Projection: projection,
	}, nil)
}

func (opts Options) ReportResourceProjection(ctx context.Context, projection controlplane.ResourceProjection) error {
	if opts.APIURL == "" || opts.ID == "" || projection.Topology == "" {
		return nil
	}
	if projection.AgentID == "" {
		projection.AgentID = opts.ID
	}
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/projections/resources", projection, nil)
}

func (opts Options) ReportNodeOperationComplete(ctx context.Context, op controlplane.NodeOperation) error {
	if opts.APIURL == "" || opts.ID == "" || op.ID == "" {
		return nil
	}
	if op.AgentID == "" {
		op.AgentID = opts.ID
	}
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/node-operations/"+op.ID+"/complete", op, nil)
}

func claim(ctx context.Context, opts Options, runID string) (*controlplane.Run, error) {
	var run controlplane.Run
	if err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+runID+"/claim", nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func get(ctx context.Context, opts Options, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	authorize(req, opts)
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

func post(ctx context.Context, opts Options, url string, in any, out any) error {
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
	authorize(req, opts)
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

func authorize(req *http.Request, opts Options) {
	if opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.Token)
	}
}
