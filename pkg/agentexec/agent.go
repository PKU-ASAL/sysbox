package agentexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

const DefaultAgentID = "local"

type Options struct {
	APIURL                           string
	Token                            string
	ID                               string
	Name                             string
	Capabilities                     []string
	Labels                           map[string]string
	Version                          string
	Secret                           string
	PollInterval                     time.Duration
	RunRenewInterval                 time.Duration
	RunRenewTTL                      time.Duration
	Policy                           config.AgentPolicyConfig
	CapacityPath                     string
	ArtifactPaths                    []string
	ReportGuestExecutionCompleteFunc func(context.Context, string, controlplane.GuestExecutionCompletion) error
	ReportGuestFileStartFunc         func(context.Context, string) error
	ReportGuestFileCompleteFunc      func(context.Context, string, controlplane.GuestFileOperationCompletion) error
	RequestTimeout                   time.Duration
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
	if opts.RunRenewInterval <= 0 {
		opts.RunRenewInterval = 30 * time.Second
	}
	if opts.RunRenewTTL <= 0 {
		opts.RunRenewTTL = 30 * time.Minute
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if len(opts.Capabilities) == 0 {
		opts.Capabilities = []string{"docker", "network", "firecracker", "kvm", "libvirt"}
	}
	if err := post(ctx, opts, opts.APIURL+"/v1/agents", controlplane.Agent{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		AuthSecret:   opts.Secret,
		SecretHash:   agent.SecretHash(opts.Secret),
		Protocol:     controlplane.AgentProtocolVersion,
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil); err != nil {
		return err
	}

	executor := NewExecutorWithBridge(remoteBridge{Bridge: bridge, reporter: opts})
	go heartbeatLoop(ctx, opts)
	go observeLoop(ctx, opts, bridge)
	runner := newCommandRunner(executor, opts)
	for {
		if err := commandWebSocketAndExecute(ctx, runner, opts); err != nil && ctx.Err() == nil {
			fmt.Printf("[agent] command websocket disconnected: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.PollInterval):
		}
	}
}

func heartbeatLoop(ctx context.Context, opts Options) {
	interval := opts.PollInterval
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	if err := heartbeat(ctx, opts); err != nil && ctx.Err() == nil {
		fmt.Printf("[agent] heartbeat failed: %v\n", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := heartbeat(ctx, opts); err != nil && ctx.Err() == nil {
				fmt.Printf("[agent] heartbeat failed: %v\n", err)
			}
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
	if err := opts.ReportInventory(ctx, Inventory(ctx, opts, bridge)); err != nil {
		fmt.Printf("[agent] report inventory failed: %v\n", err)
	}
}

func heartbeat(ctx context.Context, opts Options) error {
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/heartbeat", controlplane.Agent{
		ID:           opts.ID,
		Name:         opts.Name,
		Status:       "online",
		AuthSecret:   opts.Secret,
		SecretHash:   agent.SecretHash(opts.Secret),
		Protocol:     controlplane.AgentProtocolVersion,
		Capabilities: opts.Capabilities,
		Labels:       opts.Labels,
		Version:      opts.Version,
	}, nil)
}

func commandWebSocketAndExecute(ctx context.Context, runner *commandRunner, opts Options) error {
	wsURL := strings.TrimRight(opts.APIURL, "/") + "/v1/agents/" + opts.ID + "/commands/stream"
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	headers := http.Header{}
	if opts.Token != "" {
		headers.Set("Authorization", "Bearer "+opts.Token)
	}
	if opts.Secret != "" {
		headers.Set(agent.HeaderAgentID, opts.ID)
		headers.Set(agent.HeaderAgentTimestamp, strconv.FormatInt(time.Now().UTC().Unix(), 10))
		headers.Set(agent.HeaderAgentSignature, agent.Signature("GET", "/v1/agents/"+opts.ID+"/commands/stream", headers.Get(agent.HeaderAgentTimestamp), nil, opts.Secret))
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	reporter := &commandEventReporter{conn: conn, opts: opts}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var cmd controlplane.AgentCommand
		if err := json.Unmarshal(data, &cmd); err != nil {
			reporter.Report(ctx, controlplane.AgentCommandEvent{
				Type:    "decode",
				Status:  "failed",
				Error:   err.Error(),
				Message: "failed to decode command",
			})
			continue
		}
		reporter.Report(ctx, controlplane.AgentCommandEvent{
			CommandID: cmd.ID,
			Type:      cmd.Type,
			Status:    "ack",
			Message:   "command received",
		})
		go runner.Execute(ctx, &cmd, func(event controlplane.AgentCommandEvent) {
			reporter.Report(ctx, event)
		})
	}
}

type commandRunner struct {
	mu       sync.Mutex
	executor *Executor
	opts     Options
	running  map[string]context.CancelFunc
}

func newCommandRunner(executor *Executor, opts Options) *commandRunner {
	return &commandRunner{executor: executor, opts: opts, running: map[string]context.CancelFunc{}}
}

func (r *commandRunner) Execute(ctx context.Context, cmd *controlplane.AgentCommand, report func(controlplane.AgentCommandEvent)) {
	if cmd == nil {
		return
	}
	emit := func(status, message string, err error) {
		if report == nil {
			return
		}
		event := controlplane.AgentCommandEvent{
			CommandID: cmd.ID,
			Type:      cmd.Type,
			Status:    status,
			Message:   message,
		}
		if err != nil {
			event.Error = err.Error()
		}
		report(event)
	}
	if cmd.Type == "cancel_command" {
		target := cmd.Operation.ExternalID
		if target == "" && cmd.Session != nil {
			target = cmd.Session.ID
		}
		if r.cancel(target) {
			if report != nil {
				report(controlplane.AgentCommandEvent{
					CommandID: target,
					Type:      "cancelled",
					Status:    "cancelled",
					Message:   "command cancelled by cancel_command",
				})
			}
			emit("completed", "command cancelled", nil)
			return
		}
		emit("failed", "command not running", fmt.Errorf("command %q is not running", target))
		return
	}
	if err := authorizeAgentCommand(r.opts.Policy, cmd); err != nil {
		emit("denied", "command denied by local agent policy", err)
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.track(cmd, cancel)
	defer r.untrack(cmd)
	emit("started", "command started", nil)
	switch cmd.Type {
	case "run_assigned":
		if cmd.Run == nil {
			emit("failed", "missing run", fmt.Errorf("missing run"))
			return
		}
		claimed, err := claim(runCtx, r.opts, cmd.Run.ID)
		if err != nil {
			fmt.Printf("[agent] claim %s failed: %v\n", cmd.Run.ID, err)
			emit("failed", "run claim failed", err)
			return
		}
		stopRenew := startRunLeaseRenewal(runCtx, r.opts, claimed)
		defer stopRenew()
		r.executor.ExecuteContext(runCtx, claimed)
		emit("completed", "run command completed", nil)
	case "session_open":
		if cmd.Session == nil {
			emit("failed", "missing session", fmt.Errorf("missing session"))
			return
		}
		if err := openConsoleSession(runCtx, r.opts, r.executor.bridge, *cmd.Session, cmd.Request); err != nil {
			fmt.Printf("[agent] console session %s failed: %v\n", cmd.Session.ID, err)
			emit("failed", "console session failed", err)
			return
		}
		emit("completed", "console session completed", nil)
	case "node_operation":
		completed := r.executor.ExecuteNodeOperation(runCtx, cmd.Operation)
		if err := r.opts.ReportNodeOperationComplete(runCtx, completed); err != nil {
			fmt.Printf("[agent] report node operation %s failed: %v\n", completed.ID, err)
			emit("failed", "node operation report failed", err)
			return
		}
		if completed.Status == controlplane.NodeOperationStatusFailed {
			emit("failed", "node operation failed", errors.New(completed.Err))
			return
		}
		emit("completed", "node operation completed", nil)
	case "guest_execution":
		if cmd.Execution == nil {
			emit("failed", "missing guest execution", fmt.Errorf("missing guest execution"))
			return
		}
		if err := r.opts.ReportGuestExecutionStart(runCtx, cmd.Execution.ID); err != nil {
			emit("failed", "guest execution start report failed", err)
			return
		}
		mgr, err := r.executor.bridge.StateManager(cmd.Execution.Topology)
		if err != nil {
			emit("failed", "guest execution state unavailable", err)
			return
		}
		st, err := mgr.Load()
		if err != nil {
			emit("failed", "guest execution state unavailable", err)
			return
		}
		completed := executeGuestOperation(runCtx, st, cmd.Execution.Node, cmd.ExecutionRequest)
		if err := r.opts.ReportGuestExecutionComplete(runCtx, cmd.Execution.ID, controlplane.GuestExecutionCompletion{Result: completed.Result, ResultClass: completed.ResultClass, Error: completed.Err}); err != nil {
			emit("failed", "guest execution completion report failed", err)
			return
		}
		if completed.Err != "" {
			emit("failed", "guest execution failed", errors.New(completed.ResultClass))
			return
		}
		emit("completed", "guest execution completed", nil)
	case "guest_file_put":
		if cmd.FilePut == nil {
			emit("failed", "missing guest file", fmt.Errorf("missing guest file"))
			return
		}
		if err := r.opts.ReportGuestFileStart(runCtx, cmd.FilePut.ID); err != nil {
			emit("failed", "guest file start report failed", err)
			return
		}
		mgr, err := r.executor.bridge.StateManager(cmd.FilePut.Topology)
		if err != nil {
			if reportErr := r.opts.ReportGuestFileComplete(runCtx, cmd.FilePut.ID, controlplane.GuestFileOperationCompletion{Error: "file operation failed"}); reportErr != nil {
				emit("failed", "guest file completion report failed", reportErr)
				return
			}
			emit("failed", "guest file state unavailable", err)
			return
		}
		st, err := mgr.Load()
		if err != nil {
			if reportErr := r.opts.ReportGuestFileComplete(runCtx, cmd.FilePut.ID, controlplane.GuestFileOperationCompletion{Error: "file operation failed"}); reportErr != nil {
				emit("failed", "guest file completion report failed", reportErr)
				return
			}
			emit("failed", "guest file state unavailable", err)
			return
		}
		if err := putGuestFile(runCtx, r.opts, st, *cmd.FilePut); err != nil {
			if reportErr := r.opts.ReportGuestFileComplete(runCtx, cmd.FilePut.ID, controlplane.GuestFileOperationCompletion{Error: "file operation failed"}); reportErr != nil {
				emit("failed", "guest file completion report failed", reportErr)
				return
			}
			emit("failed", "guest file put failed", err)
			return
		}
		if err := r.opts.ReportGuestFileComplete(runCtx, cmd.FilePut.ID, controlplane.GuestFileOperationCompletion{}); err != nil {
			emit("failed", "guest file completion report failed", err)
			return
		}
		emit("completed", "guest file put completed", nil)
	default:
		err := fmt.Errorf("unknown command type %q", cmd.Type)
		fmt.Printf("[agent] %v\n", err)
		emit("failed", "unknown command type", err)
	}
}

func (r *commandRunner) track(cmd *controlplane.AgentCommand, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running[cmd.ID] = cancel
	if cmd.Session != nil && cmd.Session.ID != "" {
		r.running[cmd.Session.ID] = cancel
	}
	if cmd.Run != nil && cmd.Run.ID != "" {
		r.running[cmd.Run.ID] = cancel
	}
	if cmd.Operation.ID != "" {
		r.running[cmd.Operation.ID] = cancel
	}
	if cmd.Execution != nil && cmd.Execution.ID != "" {
		r.running[cmd.Execution.ID] = cancel
	}
	if cmd.FilePut != nil && cmd.FilePut.ID != "" {
		r.running[cmd.FilePut.ID] = cancel
	}
}

func (r *commandRunner) untrack(cmd *controlplane.AgentCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, cmd.ID)
	if cmd.Session != nil {
		delete(r.running, cmd.Session.ID)
	}
	if cmd.Run != nil {
		delete(r.running, cmd.Run.ID)
	}
	delete(r.running, cmd.Operation.ID)
	if cmd.Execution != nil {
		delete(r.running, cmd.Execution.ID)
	}
	if cmd.FilePut != nil {
		delete(r.running, cmd.FilePut.ID)
	}
}

func (r *commandRunner) cancel(id string) bool {
	if id == "" {
		return false
	}
	r.mu.Lock()
	cancel := r.running[id]
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func authorizeAgentCommand(policy config.AgentPolicyConfig, cmd *controlplane.AgentCommand) error {
	if cmd == nil {
		return fmt.Errorf("missing command")
	}
	if len(policy.AllowedCommands) > 0 && !slices.Contains(policy.AllowedCommands, cmd.Type) {
		return fmt.Errorf("command %q is not allowed", cmd.Type)
	}
	switch cmd.Type {
	case "run_assigned":
		if cmd.Run == nil {
			return fmt.Errorf("missing run")
		}
		if err := allowWorkspace(policy, cmd.Run.Workspace, cmd.Run.Topology); err != nil {
			return err
		}
	case "session_open":
		if policy.AllowConsole != nil && !*policy.AllowConsole {
			return fmt.Errorf("console sessions are disabled")
		}
		if cmd.Session == nil {
			return fmt.Errorf("missing session")
		}
		if err := allowWorkspace(policy, cmd.Session.Workspace, cmd.Session.Topology); err != nil {
			return err
		}
	case "node_operation":
		if err := allowWorkspace(policy, cmd.Operation.Workspace, cmd.Operation.Topology); err != nil {
			return err
		}
		if err := allowSubstrate(policy, cmd.Operation.Substrate); err != nil {
			return err
		}
		if cmd.Operation.Operation == "import" && policy.AllowImport != nil && !*policy.AllowImport {
			return fmt.Errorf("import is disabled")
		}
	case "guest_execution":
		if policy.AllowGuestExec != nil && !*policy.AllowGuestExec {
			return fmt.Errorf("guest execution is disabled")
		}
		if cmd.Execution == nil {
			return fmt.Errorf("missing guest execution")
		}
		if err := allowWorkspace(policy, cmd.Execution.Topology, cmd.Execution.Topology); err != nil {
			return err
		}
	case "guest_file_put":
		if policy.AllowGuestFiles != nil && !*policy.AllowGuestFiles {
			return fmt.Errorf("guest files are disabled")
		}
		if cmd.FilePut == nil {
			return fmt.Errorf("missing guest file")
		}
		if err := allowWorkspace(policy, cmd.FilePut.Topology, cmd.FilePut.Topology); err != nil {
			return err
		}
	}
	return nil
}

func allowWorkspace(policy config.AgentPolicyConfig, workspace, topology string) error {
	if len(policy.AllowedWorkspaces) == 0 {
		return nil
	}
	if workspace == "" {
		workspace = topology
	}
	if slices.Contains(policy.AllowedWorkspaces, workspace) {
		return nil
	}
	return fmt.Errorf("workspace %q is not allowed", workspace)
}

func allowSubstrate(policy config.AgentPolicyConfig, substrate string) error {
	if len(policy.AllowedSubstrates) == 0 || substrate == "" {
		return nil
	}
	if slices.Contains(policy.AllowedSubstrates, substrate) {
		return nil
	}
	return fmt.Errorf("substrate %q is not allowed", substrate)
}

type commandEventReporter struct {
	mu   sync.Mutex
	conn *websocket.Conn
	opts Options
}

func (r *commandEventReporter) Report(ctx context.Context, event controlplane.AgentCommandEvent) {
	if r == nil || r.conn == nil {
		return
	}
	if event.AgentID == "" {
		event.AgentID = r.opts.ID
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.conn.Write(ctx, websocket.MessageText, raw)
}

func openConsoleSession(ctx context.Context, opts Options, bridge Bridge, sess controlplane.ConsoleSession, req controlplane.ConsoleRequest) error {
	wsURL := strings.TrimRight(opts.APIURL, "/") + "/v1/agents/" + opts.ID + "/sessions/" + sess.ID + "/attach"
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	headers := http.Header{}
	if opts.Token != "" {
		headers.Set("Authorization", "Bearer "+opts.Token)
	}
	if opts.Secret != "" {
		headers.Set(agent.HeaderAgentID, opts.ID)
		headers.Set(agent.HeaderAgentTimestamp, strconv.FormatInt(time.Now().UTC().Unix(), 10))
		headers.Set(agent.HeaderAgentSignature, agent.Signature("GET", "/v1/agents/"+opts.ID+"/sessions/"+sess.ID+"/attach", headers.Get(agent.HeaderAgentTimestamp), nil, opts.Secret))
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := bridge.OpenConsole(ctx, sess, req, conn); err != nil {
		_ = writeConsoleFrame(ctx, conn, consoleFrame{Type: "error", Error: err.Error()})
		return err
	}
	return nil
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

func (opts Options) ReportInventory(ctx context.Context, inv controlplane.AgentInventory) error {
	if opts.APIURL == "" || opts.ID == "" {
		return nil
	}
	if inv.AgentID == "" {
		inv.AgentID = opts.ID
	}
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/inventory", inv, nil)
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

func (opts Options) ReportGuestExecutionComplete(ctx context.Context, id string, completion controlplane.GuestExecutionCompletion) error {
	if opts.ReportGuestExecutionCompleteFunc != nil {
		return opts.ReportGuestExecutionCompleteFunc(ctx, id, completion)
	}
	if opts.APIURL == "" || opts.ID == "" || id == "" {
		return nil
	}
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/guest-executions/"+id+"/complete", completion, nil)
}

func (opts Options) ReportGuestExecutionStart(ctx context.Context, id string) error {
	if opts.APIURL == "" || opts.ID == "" || id == "" {
		return nil
	}
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/guest-executions/"+id+"/start", nil, nil)
}

func (opts Options) ReportGuestFileStart(ctx context.Context, id string) error {
	return retryGuestFileReport(ctx, func() error {
		if opts.ReportGuestFileStartFunc != nil {
			return opts.ReportGuestFileStartFunc(ctx, id)
		}
		if opts.APIURL == "" || opts.ID == "" || id == "" {
			return nil
		}
		return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/guest-file-operations/"+id+"/start", nil, nil)
	})
}

func (opts Options) ReportGuestFileComplete(ctx context.Context, id string, completion controlplane.GuestFileOperationCompletion) error {
	return retryGuestFileReport(ctx, func() error {
		if opts.ReportGuestFileCompleteFunc != nil {
			return opts.ReportGuestFileCompleteFunc(ctx, id, completion)
		}
		if opts.APIURL == "" || opts.ID == "" || id == "" {
			return nil
		}
		return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/guest-file-operations/"+id+"/complete", completion, nil)
	})
}

func retryGuestFileReport(ctx context.Context, report func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = report(); err == nil {
			return nil
		}
		if attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return err
}

func claim(ctx context.Context, opts Options, runID string) (*controlplane.Run, error) {
	var run controlplane.Run
	if err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+runID+"/claim", nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func renewRunLease(ctx context.Context, opts Options, run *controlplane.Run) error {
	if run == nil || run.ID == "" || run.LeaseOwner == "" {
		return nil
	}
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+run.ID+"/renew", map[string]any{
		"lease_owner": run.LeaseOwner,
		"ttl_seconds": int(opts.RunRenewTTL.Seconds()),
	}, run)
}

func startRunLeaseRenewal(ctx context.Context, opts Options, run *controlplane.Run) func() {
	renewCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(opts.RunRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if err := renewRunLease(renewCtx, opts, run); err != nil && renewCtx.Err() == nil {
					fmt.Printf("[agent] renew run lease %s failed: %v\n", run.ID, err)
				}
			}
		}
	}()
	return cancel
}

func requestClient(opts Options) *http.Client {
	timeout := opts.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func get(ctx context.Context, opts Options, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	authorize(req, opts)
	resp, err := requestClient(opts).Do(req)
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
	resp, err := requestClient(opts).Do(req)
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
	_ = agent.SignRequest(req, opts.ID, opts.Secret, time.Now())
}
