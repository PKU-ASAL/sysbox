package agentexec

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/agent"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

const (
	DefaultAgentID                 = "local"
	agentCommandWebSocketReadLimit = 4 << 20
)

type Options struct {
	APIURL                           string
	Token                            string
	SessionID                        string
	ID                               string
	Name                             string
	Capabilities                     []string
	SessionFreshness                 bool
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
	if opts.SessionID == "" {
		opts.SessionID = uuid.NewString()
	}
	opts.httpClient = &http.Client{Timeout: opts.RequestTimeout}
	opts.APIURL = strings.TrimRight(opts.APIURL, "/")
	if len(opts.Capabilities) == 0 {
		opts.Capabilities, _ = agent.DetectCapabilities()
	}
	if opts.SessionFreshness {
		// VM health projections must belong to the current agent process.
		session := make([]byte, 16)
		if _, err := rand.Read(session); err != nil {
			return fmt.Errorf("create agent session: %w", err)
		}
		labels := make(map[string]string, len(opts.Labels)+1)
		for key, value := range opts.Labels {
			labels[key] = value
		}
		labels[controlplane.AgentSessionLabel] = fmt.Sprintf("%x", session)
		opts.Labels = labels
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
	conn.SetReadLimit(agentCommandWebSocketReadLimit)
	defer conn.Close(websocket.StatusNormalClosure, "")
	reporter := &commandEventReporter{conn: conn, opts: opts}
	for {
		data, err := readAgentCommandMessage(ctx, conn)
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
		go runner.Execute(ctx, &cmd, func(event controlplane.AgentCommandEvent) {
			reporter.Report(ctx, event)
		})
	}
}

func readAgentCommandMessage(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
	conn.SetReadLimit(agentCommandWebSocketReadLimit)
	_, data, err := conn.Read(ctx)
	return data, err
}

type commandRunner struct {
	mu       sync.Mutex
	executor *Executor
	opts     Options
	running  map[string]context.CancelFunc
}

func newCommandRunner(executor *Executor, opts Options) *commandRunner {
	if opts.SessionID == "" {
		opts.SessionID = uuid.NewString()
	}
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
			LeaseOwner: cmd.LeaseOwner,
			CommandID:  cmd.ID,
			Type:       cmd.Type,
			Status:     status,
			Message:    message,
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
	defer cancel()
	if !r.track(cmd, cancel) {
		return
	}
	defer r.untrack(cmd)
	path := r.executor.bridge.CheckpointFile("_agent_commands", commandInboxKey(cmd.ID))
	if err := acceptCommand(path, r.opts.SessionID, cmd); err != nil {
		emit("failed", "durable command acceptance failed", err)
		return
	}
	if cmd.Type != "run_assigned" {
		bounded, stop := context.WithTimeout(runCtx, controlplane.AgentCommandExecutionLimit)
		defer stop()
		runCtx = bounded
		emit("ack", "command durably accepted", nil)
		emit("started", "command started", nil)
	}
	switch cmd.Type {
	case "run_assigned":
		if cmd.Run == nil {
			emit("failed", "missing run", fmt.Errorf("missing run"))
			return
		}
		claimCtx := runCtx
		if !cmd.Run.DeadlineAt.IsZero() {
			var cancelClaim context.CancelFunc
			claimCtx, cancelClaim = context.WithDeadline(runCtx, cmd.Run.DeadlineAt)
			defer cancelClaim()
		}
		claimed, err := claim(claimCtx, r.opts, cmd.Run.ID)
		if err != nil {
			fmt.Printf("[agent] claim %s failed: %v\n", cmd.Run.ID, err)
			emit("failed", "run claim failed", err)
			return
		}
		emit("ack", "run claim durably accepted", nil)
		emit("started", "run started", nil)
		stopRenew := startRunLeaseRenewal(runCtx, r.opts, claimed, cancel)
		defer stopRenew()
		r.executor.ExecuteContext(runCtx, claimed)
		if claimed.Status == controlplane.RunDone {
			emit("completed", "run command completed", nil)
		} else {
			emit("failed", "run requires recovery", errors.New(claimed.Err))
		}
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
		mgr, err := r.executor.bridge.StateManager(cmd.Execution.Topology)
		if err != nil {
			emit("failed", "guest execution state unavailable", err)
			return
		}
		st, err := mgr.LoadWithContext(opCtx)
		if err != nil {
			emit("failed", "guest execution state unavailable", err)
			return
		}
		completed := executeGuestOperation(opCtx, st, cmd.Execution.Node, cmd.ExecutionRequest)
		// Report completion on runCtx, not opCtx: the operation's own deadline
		// has very likely expired by now, and the result must still be
		// delivered — otherwise a timed-out execution is never reported at all
		// and the caller is left polling something that will never resolve.
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

func commandKeys(cmd *controlplane.AgentCommand) []string {
	keys := []string{cmd.ID}
	if cmd.Session != nil {
		keys = append(keys, cmd.Session.ID)
	}
	if cmd.Run != nil {
		keys = append(keys, cmd.Run.ID)
	}
	keys = append(keys, cmd.Operation.ID)
	if cmd.Execution != nil {
		keys = append(keys, cmd.Execution.ID)
	}
	if cmd.FilePut != nil {
		keys = append(keys, cmd.FilePut.ID)
	}
	return keys
}
func (r *commandRunner) track(cmd *controlplane.AgentCommand, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := commandKeys(cmd)
	for _, key := range keys {
		if key != "" {
			if _, exists := r.running[key]; exists {
				return false
			}
		}
	}
	for _, key := range keys {
		if key != "" {
			r.running[key] = cancel
		}
	}
	return true
}
func (r *commandRunner) untrack(cmd *controlplane.AgentCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range commandKeys(cmd) {
		if key != "" {
			delete(r.running, key)
		}
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
	if run != nil && b.reporter.APIURL != "" && b.reporter.ID != "" && topologyHasVM(ctx, b.Bridge, run.Topology) {
		resources, err := observeTopology(ctx, b.reporter.ID, b.Bridge, run.Topology)
		if err != nil {
			return err
		}
		if err := b.reporter.ReportResourceProjection(ctx, resources); err != nil {
			return err
		}
	}
	return b.reporter.ReportRunComplete(ctx, run, projection)
}

func topologyHasVM(ctx context.Context, bridge Bridge, topology string) bool {
	if bridge == nil || topology == "" {
		return false
	}
	mgr, err := bridge.StateManager(topology)
	if err != nil {
		return false
	}
	st, err := mgr.LoadWithContext(ctx)
	return err == nil && stateHasVM(st)
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
	// Session freshness is needed for VM observations. Keep legacy Docker
	// projection payloads unchanged.
	for _, resource := range append(append([]controlplane.ResourceHealth{}, projection.Resources...), projection.Health.Resources...) {
		if resource.Provider == "firecracker" || resource.Provider == "libvirt" {
			projection.SessionID = opts.Labels[controlplane.AgentSessionLabel]
			break
		}
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

type postStatusError struct {
	code    int
	message string
}

func (e *postStatusError) Error() string { return e.message }
func claim(ctx context.Context, opts Options, runID string) (*controlplane.Run, error) {
	// A permanently unavailable API must not retain this command indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// A new execution instance gets a new identity; transport retries reuse it.
	request := map[string]string{"claim_id": uuid.NewSHA1(uuid.NameSpaceOID, []byte(opts.SessionID+":"+uuid.NewString())).String()}
	for {
		var run controlplane.Run
		err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+runID+"/claim", request, &run)
		if err == nil {
			return &run, nil
		}
		var status *postStatusError
		// Any transport/response decoding failure has unknown delivery. Replay the
		// same claim; explicit protocol rejection must never start another attempt.
		if errors.As(err, &status) && status.code != http.StatusServiceUnavailable && status.code != http.StatusBadGateway && status.code != http.StatusGatewayTimeout {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func renewRunLease(ctx context.Context, opts Options, run controlplane.Run) (time.Time, error) {
	var renewed controlplane.Run
	err := post(ctx, opts, opts.APIURL+"/v1/agents/"+opts.ID+"/runs/"+run.ID+"/renew", map[string]any{"lease_owner": run.LeaseOwner, "ttl_seconds": int(opts.RunRenewTTL.Seconds())}, &renewed)
	if err != nil {
		return time.Time{}, err
	}
	if renewed.ID != run.ID || renewed.LeaseOwner != run.LeaseOwner || renewed.Attempt != run.Attempt || !renewed.LeaseUntil.After(time.Now()) {
		return time.Time{}, fmt.Errorf("renewal ownership mismatch")
	}
	return renewed.LeaseUntil, nil
}

// A lost renewal is an uncertain authority result, never permission to keep
// mutating resources. The request budget also cannot outlive the last lease.
func startRunLeaseRenewal(ctx context.Context, opts Options, run *controlplane.Run, onLost context.CancelFunc) func() {
	renewCtx, cancel := context.WithCancel(ctx)
	snapshot := *run
	// Respect a shorter API-granted lease in crash tests and deployments; the
	// agent default must not silently turn a 10s claim into a 30m renewal.
	remaining := time.Until(snapshot.LeaseUntil)
	grantTTL := ((remaining + time.Second - 1) / time.Second) * time.Second
	if grantTTL > 0 && (opts.RunRenewTTL <= 0 || opts.RunRenewTTL > grantTTL) {
		opts.RunRenewTTL = grantTTL
	}
	go func() {
		deadline := snapshot.LeaseUntil
		interval := opts.RunRenewInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				onLost()
				return
			}
			wait := min(interval, remaining/2)
			timer := time.NewTimer(wait)
			select {
			case <-renewCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			attempt, stop := context.WithDeadline(renewCtx, deadline)
			next, err := renewRunLease(attempt, opts, snapshot)
			stop()
			if err != nil {
				if renewCtx.Err() == nil {
					onLost()
				}
				return
			}
			deadline = next
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
		return &postStatusError{code: resp.StatusCode, message: fmt.Sprintf("POST %s: %s", url, resp.Status)}
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
