package agentexec

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
)

const DefaultAgentID = "local"

type Options struct {
	HostSupervisorSocket string
	// RequireHostSupervisor is enabled by the production Agent command. Local
	// unit fixtures may leave it false to exercise the explicit in-process CLI
	// executor without making that path a remote control-plane fallback.
	RequireHostSupervisor       bool
	observationSequence         *atomic.Uint64
	ExecutionSessionFile        string
	ExecutionSessionID          string
	ExecutionGrantPublicKeyFile string
	APIURL                      string
	Token                       string
	ID                          string
	Name                        string
	Capabilities                []string
	Labels                      map[string]string
	Version                     string
	Secret                      string
	PollInterval                time.Duration
	RunRenewInterval            time.Duration
	RunRenewTTL                 time.Duration
	Policy                      config.AgentPolicyConfig
	CapacityPath                string
	ArtifactPaths               []string
	ReportGuestFileStartFunc    func(context.Context, string) error
	RequestTimeout              time.Duration

	// httpClient is the shared HTTP client for agent → API requests. Run builds
	// it once so keep-alive connections are reused across heartbeats, projections
	// and completion reports instead of re-dialling per request.
	httpClient *http.Client
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
	opts.httpClient = &http.Client{Timeout: opts.RequestTimeout}
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if len(opts.Capabilities) == 0 {
		opts.Capabilities, _ = agent.DetectCapabilities()
	}
	if opts.HostSupervisorSocket != "" && !slices.Contains(opts.Capabilities, controlplane.RunRecoveryCapability) {
		opts.Capabilities = append(opts.Capabilities, controlplane.RunRecoveryCapability)
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
	if opts.ExecutionSessionFile == "" {
		opts.ExecutionSessionFile = filepath.Join(config.DefaultRunsDir(), "_agents", opts.ID, "execution-session.json")
	}
	session, err := establishExecutionSession(ctx, opts)
	if err != nil {
		return err
	}
	opts.ExecutionSessionID = session.ID
	opts.observationSequence = &atomic.Uint64{}

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
			ActionID:  cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, LeaseEpoch: cmd.LeaseEpoch,
			LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt,
			Type:    cmd.Type,
			Status:  "ack",
			Message: "command received",
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
	running  map[string]*trackedCommand
}

type trackedCommand struct {
	cancel context.CancelFunc
	keys   []string
}

func newCommandRunner(executor *Executor, opts Options) *commandRunner {
	return &commandRunner{executor: executor, opts: opts, running: map[string]*trackedCommand{}}
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
			ActionID:  cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, LeaseEpoch: cmd.LeaseEpoch,
			LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt,
			Type:    cmd.Type,
			Status:  status,
			Message: message,
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
			// The target still owns its outcome. Cancelling its context does not
			// prove its effects stopped or authorize retiring its delivery lease.
			emit("completed", "cancellation requested", nil)
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
	tracked := r.track(cmd, cancel)
	defer r.untrack(tracked)
	emit("started", "command started", nil)
	switch cmd.Type {
	case controlplane.RunRecoveryCommand:
		if !cmd.LeaseUntil.After(time.Now()) {
			emit("failed", "recovery delivery lease expired", context.DeadlineExceeded)
			return
		}
		recoveryCtx, stopRecovery := context.WithDeadline(runCtx, cmd.LeaseUntil)
		defer stopRecovery()
		if r.opts.HostSupervisorSocket == "" {
			emit("failed", "host supervisor is required for remote recovery", fmt.Errorf("host supervisor socket is not configured"))
			return
		}
		proof := hostsupervisor.RunRecoveryRequest{CommandID: cmd.ID, LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt}
		result, err := hostsupervisor.NewTaskClient(r.opts.HostSupervisorSocket, hostsupervisor.TargetRef{}).RecoverRun(recoveryCtx, proof)
		if err != nil {
			emit("failed", "owning-agent recovery uncertain", err)
			return
		}
		if report != nil {
			report(controlplane.AgentCommandEvent{AgentID: r.opts.ID, CommandID: cmd.ID, Type: cmd.Type, Status: "completed", LeaseOwner: cmd.LeaseOwner, Attempt: cmd.Attempt, Recovery: result})
		}
	case "run_assigned":
		if cmd.Run == nil {
			emit("failed", "missing run", fmt.Errorf("missing run"))
			return
		}
		claimed, err := claim(runCtx, r.opts, *cmd.Run)
		if err != nil {
			fmt.Printf("[agent] claim %s failed: %v\n", cmd.Run.ID, err)
			emit("failed", "run claim failed", err)
			return
		}
		stopRenew := startRunLeaseRenewal(runCtx, r.opts, claimed)
		defer stopRenew()
		if r.opts.RequireHostSupervisor && r.opts.HostSupervisorSocket == "" {
			emit("failed", "host supervisor is required for remote Run", fmt.Errorf("host supervisor socket is not configured"))
			return
		}
		if r.opts.HostSupervisorSocket != "" {
			completed, err := r.executeAssignedHostRun(runCtx, *claimed, cmd, emit)
			if err != nil {
				emit("failed", "host supervisor run failed", err)
				return
			}
			if completed == nil {
				emit("started", "host supervisor accepted run; awaiting host receipt", nil)
			} else if completed.Status == controlplane.RunDone {
				emit("completed", "host supervisor run completed", nil)
			} else {
				emit("failed", "host supervisor run failed", errors.New(completed.Err))
			}
			return
		}
		runCtx, err = r.prepareRunWorkspace(runCtx, *claimed)
		if err != nil {
			r.executor.bridge.AttachRun(claimed)
			r.executor.bridge.Finish(claimed, err)
			r.executor.reportCompletion(runCtx, claimed)
			emit("failed", "run revision delivery failed", err)
			return
		}
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
		cmd.Operation.CommandID = cmd.ID
		cmd.Operation.ActionID, cmd.Operation.ResourceUID, cmd.Operation.Generation = cmd.ActionID, cmd.ResourceUID, cmd.Generation
		cmd.Operation.LeaseOwner, cmd.Operation.LeaseEpoch, cmd.Operation.Attempt = cmd.LeaseOwner, cmd.LeaseEpoch, cmd.Attempt
		completed := executeGrantedNodeOperation(runCtx, r.opts, *cmd)
		if cmd.Operation.Operation == "adoption_apply" || cmd.Operation.Operation == "ssh_access_apply" || cmd.Operation.Operation == "ssh_access_delete" {
			// The host signer owns canonical completion. Worker events report
			// delivery/execution progress only, never a substitute result.
			if completed.Status == controlplane.NodeOperationStatusFailed {
				emit("failed", "host effect outcome unconfirmed", errors.New(completed.Err))
				return
			}
			emit("completed", "host operation completion reported", nil)
			return
		}
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
		// Start the declared budget before any of the work below: the start
		// report and the state load are part of the operation the caller timed,
		// and a wedged state backend here would otherwise burn unbounded time
		// before the deadline even exists.
		opCtx, opCancel := guestOperationContext(runCtx, cmd.ExecutionRequest)
		defer opCancel()
		if err := r.opts.ReportGuestExecutionStart(opCtx, cmd.Execution.ID); err != nil {
			emit("failed", "guest execution start report failed", err)
			return
		}
		completed := executeGrantedGuestOperation(opCtx, r.opts, *cmd)
		// The host owns durable execution results and retries its signed report.
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
		if _, err := runGrantedFileInstall(runCtx, r.opts, *cmd); err != nil {
			emit("failed", "guest file installation outcome unconfirmed", err)
			return
		}
		// Only the credential-owning host receipt can complete file metadata.
		// A worker acknowledgement is delivery evidence, not installation proof.
		emit("completed", "guest file put completed", nil)
	default:
		err := fmt.Errorf("unknown command type %q", cmd.Type)
		fmt.Printf("[agent] %v\n", err)
		emit("failed", "unknown command type", err)
	}
}

func (r *commandRunner) track(cmd *controlplane.AgentCommand, cancel context.CancelFunc) *trackedCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	tracked := &trackedCommand{cancel: cancel}
	add := func(id string) {
		if id != "" {
			tracked.keys = append(tracked.keys, id)
			r.running[id] = tracked
		}
	}
	add(cmd.ID)
	if cmd.Type != controlplane.RunRecoveryCommand {
		if cmd.Session != nil {
			add(cmd.Session.ID)
		}
		if cmd.Run != nil {
			add(cmd.Run.ID)
		}
		add(cmd.Operation.ID)
		if cmd.Execution != nil {
			add(cmd.Execution.ID)
		}
		if cmd.FilePut != nil {
			add(cmd.FilePut.ID)
		}
	}
	return tracked
}

func (r *commandRunner) untrack(tracked *trackedCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range tracked.keys {
		if r.running[id] == tracked {
			delete(r.running, id)
		}
	}
}

func (r *commandRunner) cancel(id string) bool {
	if id == "" {
		return false
	}
	r.mu.Lock()
	tracked, ok := r.running[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	tracked.cancel()
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
	case "run_assigned", controlplane.RunRecoveryCommand:
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
	reported := *run
	reported.Cleanup = nil
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+run.ID+"/complete", controlplane.RunCompletion{
		Cleanup:    run.Cleanup,
		Run:        reported,
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
	if opts.ExecutionSessionID == "" || opts.observationSequence == nil {
		return fmt.Errorf("observation requires execution session")
	}
	projection.SessionID = opts.ExecutionSessionID
	projection.Sequence = opts.observationSequence.Add(1)
	return post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/projections/resources", projection, nil)
}

func (opts Options) ReportInventory(ctx context.Context, inv controlplane.AgentInventory) error {
	if opts.APIURL == "" || opts.ID == "" {
		return nil
	}
	if inv.AgentID == "" {
		inv.AgentID = opts.ID
	}
	if opts.ExecutionSessionID == "" || opts.observationSequence == nil {
		return fmt.Errorf("observation requires execution session")
	}
	inv.SessionID = opts.ExecutionSessionID
	inv.Sequence = opts.observationSequence.Add(1)
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

func claim(ctx context.Context, opts Options, expected controlplane.Run) (*controlplane.Run, error) {
	for {
		var run controlplane.Run
		err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+expected.ID+"/claim", controlplane.RunClaim{QueuedAt: expected.QueuedAt, Revision: expected.Revision}, &run)
		if err == nil {
			return &run, nil
		}
		var responseErr *httpStatusError
		if !errors.As(err, &responseErr) || responseErr.status != http.StatusServiceUnavailable {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

type httpStatusError struct {
	status  int
	message string
}

func (e *httpStatusError) Error() string { return e.message }

func renewRunLease(ctx context.Context, opts Options, run *controlplane.Run) error {
	if run == nil || run.ID == "" || run.LeaseOwner == "" {
		return nil
	}
	var renewed controlplane.Run
	if err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+run.ID+"/renew", map[string]any{
		"lease_owner": run.LeaseOwner,
		"ttl_seconds": int(opts.RunRenewTTL.Seconds()),
	}, &renewed); err != nil {
		return err
	}
	if !persistence.SameRunIncarnation(*run, renewed) || renewed.LeaseOwner != run.LeaseOwner || renewed.Attempt != run.Attempt || renewed.Status != controlplane.RunRunning || !renewed.LeaseUntil.After(time.Now()) {
		return fmt.Errorf("run renewal identity or lease mismatch")
	}
	return nil
}

func startRunLeaseRenewal(ctx context.Context, opts Options, run *controlplane.Run) func() {
	renewCtx, cancel := context.WithCancel(ctx)
	// The executor mutates its own Run as work completes. Renewal owns a fixed
	// claim copy and never writes the API response into that executing object.
	claim := *run
	go func() {
		ticker := time.NewTicker(opts.RunRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if err := renewRunLease(renewCtx, opts, &claim); err != nil && renewCtx.Err() == nil {
					fmt.Printf("[agent] renew run lease %s failed: %v\n", claim.ID, err)
				}
			}
		}
	}()
	return cancel
}

func requestClient(opts Options) *http.Client {
	if opts.httpClient != nil {
		return opts.httpClient
	}
	// Fallback for callers that build Options directly without Run (tests).
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
		return &httpStatusError{status: resp.StatusCode, message: fmt.Sprintf("POST %s: %s", url, resp.Status)}
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

func (r *commandRunner) executeAssignedHostRun(ctx context.Context, run controlplane.Run, cmd *controlplane.AgentCommand, emit func(string, string, error)) (*controlplane.Run, error) {
	if cmd == nil || cmd.Run == nil || r.opts.ExecutionSessionID == "" || r.opts.ExecutionGrantPublicKeyFile == "" {
		return nil, fmt.Errorf("host supervisor run requires execution session and grant key")
	}
	action := run.Op
	if action == "" {
		action = run.Operation
	}
	if !controlplane.IsRunExecutionOperation("run_" + action) {
		return nil, fmt.Errorf("host supervisor does not support run operation %q", run.Op)
	}
	key, err := os.ReadFile(r.opts.ExecutionGrantPublicKeyFile)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, controlplane.ErrInvalidExecutionGrant
	}
	node := run.Target
	if i := strings.LastIndex(node, "."); i >= 0 {
		node = node[i+1:]
	}
	if node == "" && cmd.ResourceUID != "" {
		return nil, fmt.Errorf("host supervisor run requires a single resource target")
	}
	target := hostsupervisor.TargetRef{Topology: run.Topology, Node: node, Scope: controlplane.ExecutionScope{TopologyUID: cmd.TopologyUID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation}}
	req := hostsupervisor.RunRequest{RunID: run.ID, Topology: run.Topology, TopologyUID: cmd.TopologyUID, Revision: run.Revision, ActionID: cmd.ActionID, ResourceUID: cmd.ResourceUID, Generation: cmd.Generation, Action: action, Target: run.Target, ExpectedSerial: cmd.ExpectedSerial}
	digest, err := req.Digest()
	if err != nil {
		return nil, err
	}
	client := hostsupervisor.NewTaskClient(r.opts.HostSupervisorSocket, target)
	boot, err := client.RunHostBootID(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := r.opts.APIURL + "/v1/agents/" + r.opts.ID + "/commands/" + cmd.ID + "/execution-grant"
	var grant controlplane.SignedExecutionGrant
	if err := post(ctx, r.opts, endpoint, map[string]any{"session_id": r.opts.ExecutionSessionID, "lease_owner": cmd.LeaseOwner, "attempt": cmd.Attempt, "boot_id": boot}, &grant); err != nil {
		return nil, err
	}
	if err := controlplane.VerifyExecutionGrant(ed25519.PublicKey(key), grant, boot, time.Now()); err != nil {
		return nil, err
	}
	if grant.Grant.Operation != "run_"+action || grant.Grant.Digest != digest || grant.Grant.ActionID != cmd.ActionID || grant.Grant.Scope != target.Scope || grant.Grant.AgentID != r.opts.ID || grant.Grant.SessionID != r.opts.ExecutionSessionID || grant.Grant.CommandID != cmd.ID || grant.Grant.Attempt != uint64(cmd.Attempt) || grant.Grant.TargetBootID != boot {
		return nil, controlplane.ErrInvalidExecutionGrant
	}
	renew := func(ctx context.Context, current controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error) {
		var next controlplane.SignedExecutionGrant
		err := post(ctx, r.opts, endpoint+"/renew", map[string]any{"grant": current, "lease_owner": cmd.LeaseOwner}, &next)
		return next, err
	}
	return awaitHostRun(ctx, client, req, run, grant, ed25519.PublicKey(key), renew)
}

// The wire protocol carries only the durable terminal Run as evidence.
func decodeHostRunEvidence(raw json.RawMessage) (*controlplane.Run, error) {
	var run controlplane.Run
	if json.Unmarshal(raw, &run) == nil && run.ID != "" && run.Status.IsTerminal() {
		return &run, nil
	}
	return nil, fmt.Errorf("host supervisor returned non-terminal run evidence")
}
