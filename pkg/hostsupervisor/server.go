package hostsupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Server is a host-local Unix socket boundary. The socket owner/group defines
// which worker may submit requests; issuer signatures and canonical resource
// identity still authorize every effect. Private keys never cross this socket.
type Server struct {
	OwnershipPrepare func(context.Context, controlplane.OwnershipTransferPlan) (controlplane.OwnershipPreparation, error)
	GCFencedEffect   func(context.Context, GCCandidate, controlplane.SignedExecutionGrant) (json.RawMessage, error)
	AdoptionInspect  func(context.Context, controlplane.AdoptionInspectionRequest) (substrate.AdoptionEvidence, error)
	Socket           string
	GroupID          int
	Dispatcher       *Dispatcher
	Power            *PowerDispatcher
	Files            *FileDispatcher
	Runs             *RunCoordinator
	RunEffect        func(context.Context, RunRequest) (json.RawMessage, error)
	RunRecovery      func(context.Context, RunRecoveryRequest) (*controlplane.RunRecoveryReport, error)
	SSHGuest         *SSHGuestDispatcher
	Adoption         *AdoptionCoordinator
	AdoptionApply    func(context.Context, controlplane.AdoptionRequest, controlplane.SignedExecutionGrant) (json.RawMessage, error)
	GC               *GCCoordinator
	GCValidateTarget func(context.Context, TargetRef, GCCandidate, controlplane.SignedExecutionGrant) error
	// GCEffect runs inside GC.Delete; it must never invoke another coordinator.
	GCEffect        func(context.Context, GCCandidate) (json.RawMessage, error)
	OnRecoveryError func(error)
}

func (s *Server) Serve(ctx context.Context) error {
	if s.Dispatcher == nil && s.Power == nil && s.Files == nil && s.Runs == nil && s.RunRecovery == nil && s.SSHGuest == nil && s.Adoption == nil && s.GC == nil || !filepath.IsAbs(s.Socket) {
		return fmt.Errorf("absolute supervisor socket and dispatcher required")
	}
	directory := filepath.Dir(s.Socket)
	if err := EnsureOwnedDirectory(directory, 0750); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("supervisor socket directory must not be writable by worker")
	}
	lock := flock.New(s.Socket + ".lock")
	defer lock.Close()
	held, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !held {
		return fmt.Errorf("host supervisor already running")
	}
	if s.GroupID >= 0 {
		if err := os.Chown(directory, -1, s.GroupID); err != nil {
			return err
		}
	}
	if st, err := os.Lstat(s.Socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("supervisor socket path is not a socket")
		}
		// Only recover stale socket while holding this service's stable file lock.
		c, e := net.DialTimeout("unix", s.Socket, 100*time.Millisecond)
		if e == nil {
			c.Close()
			return fmt.Errorf("supervisor socket is active")
		}
		if err := os.Remove(s.Socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.Socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if s.GroupID >= 0 {
		if err := os.Chown(s.Socket, -1, s.GroupID); err != nil {
			return err
		}
	}
	if err := os.Chmod(s.Socket, 0660); err != nil {
		return err
	}
	return s.serveListener(ctx, listener)
}

func (s *Server) serveListener(parent context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer func() { cancel(); _ = listener.Close(); workers.Wait() }()
	if s.Dispatcher != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.recoveryLoop(ctx) }()
	}
	if s.Power != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, "power", s.Power.Recover) }()
	}
	if s.Files != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, "files", s.Files.Recover) }()
	}
	if s.Runs != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, "runs", s.Runs.Recover) }()
	}
	if s.Adoption != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, "adoption", s.Adoption.Recover) }()
	}
	if s.SSHGuest != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, "guest-ssh", s.SSHGuest.Recover) }()
	}
	if s.GC != nil && s.GC.ReportStop != nil {
		workers.Add(1)
		go func() { defer workers.Done(); s.runRecovery(ctx, "gc", s.GC.Recover) }()
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		workers.Add(1)
		go func() { defer workers.Done(); defer conn.Close(); s.serveConnection(ctx, conn) }()
	}
}
func (s *Server) recoveryLoop(ctx context.Context) {
	s.runRecovery(ctx, "guest-tasks", s.Dispatcher.Recover)
}
func (s *Server) runRecovery(ctx context.Context, component string, recover func(context.Context) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		// Recover bounds each record independently so a slow first record
		// cannot repeatedly consume the deadline of all later records.
		err := recover(ctx)
		if err != nil && ctx.Err() == nil && s.OnRecoveryError != nil {
			s.OnRecoveryError(fmt.Errorf("%s recovery: %w", component, err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) serveConnection(parent context.Context, conn net.Conn) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	request := Request{}
	decoder := json.NewDecoder(io.LimitReader(conn, 16<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		json.NewEncoder(conn).Encode(Response{Version: 1, Error: "invalid host supervisor request"})
		return
	}
	if request.Operation == "run-recovery" {
		// Bound unauthenticated input decoding, then let the trusted recovery
		// callback apply the canonical delivery lease. Configured leases may
		// exceed the usual short RPC budget.
		if !stop() || ctx.Err() != nil {
			return
		}
		cancel()
		ctx = parent
		stopRecovery := context.AfterFunc(parent, func() { conn.Close() })
		defer stopRecovery()
	}
	response := s.handle(ctx, parent, request)
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_ = json.NewEncoder(conn).Encode(response)
}
func (s *Server) Handle(ctx context.Context, request Request) Response {
	return s.handle(ctx, ctx, request)
}

func (s *Server) handle(ctx, serviceCtx context.Context, request Request) Response {
	response := Response{Version: 1}
	if request.Operation == "ownership-prepare" {
		if request.Version != 1 || request.Target != (TargetRef{}) || request.OwnershipPlan == nil || s.OwnershipPrepare == nil {
			response.Error = "ownership preparation unavailable"
			return response
		}
		proof, err := s.OwnershipPrepare(ctx, *request.OwnershipPlan)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.OwnershipPreparation = &proof
		}
		return response
	}

	if request.Operation == "gc-disposition" {
		if request.Version != 1 || request.Target != (TargetRef{}) || !recoveryCommandID.MatchString(request.GCFenceID) || s.GC == nil {
			response.Error = "valid canonical GC fence required"
			return response
		}
		proof, err := s.GC.Disposition(ctx, request.GCFenceID)
		if err != nil {
			response.Error = err.Error()
		} else {
			response.GCDisposition = &proof
		}
		return response
	}
	if request.Operation == "run-recovery" {
		// Scope comes from canonical command authorization, not worker input.
		if request.Version != 1 || request.Target != (TargetRef{}) || s.RunRecovery == nil || request.RecoveryRequest == nil || request.RecoveryRequest.Validate() != nil {
			response.Error = "valid host recovery command proof required"
			return response
		}
		var err error
		response.RecoveryReport, err = s.RunRecovery(ctx, *request.RecoveryRequest)
		if err != nil {
			response.Error = err.Error()
		}
		return response
	}
	isRun := request.Operation == "run-info" || request.Operation == "run-start" || request.Operation == "run-observe" || request.Operation == "run-renew" || request.Operation == "run-cancel"
	topologyRun := isRun && request.Target.Node == "" && request.Target.Scope.ResourceUID == "" && request.Target.Scope.Generation == 0
	resourceTarget := request.Target.Node != "" && request.Target.Scope.ResourceUID != "" && request.Target.Scope.Generation > 0
	if request.Version != 1 || request.Target.Topology == "" || strings.ContainsAny(request.Target.Topology, "/\\\x00") || request.Target.Scope.TopologyUID == "" || (!topologyRun && !resourceTarget) {
		response.Error = "invalid supervisor target or protocol"
		return response
	}
	var err error
	var record TaskRecord
	switch request.Operation {
	case "ssh-guest-mutate", "ssh-guest-observe":
		if request.SSHMutation == nil || request.Grant == nil {
			response.Error = "guest SSH mutation and grant required"
			return response
		}
	case "gc-delete", "gc-observe":
		c := request.GCCandidate
		if c == nil || c.TopologyUID != request.Target.Scope.TopologyUID || c.ResourceUID != request.Target.Scope.ResourceUID || c.Generation != request.Target.Scope.Generation {
			response.Error = "GC target mismatch"
			return response
		}
	case "adoption-inspect":
		r := request.AdoptionInspection
		if r == nil || r.Topology != request.Target.Topology || r.TopologyUID != request.Target.Scope.TopologyUID || r.ResourceUID != request.Target.Scope.ResourceUID || r.Generation != request.Target.Scope.Generation {
			response.Error = "adoption inspection target mismatch"
			return response
		}
	case "adoption-apply", "adoption-observe":
		r := request.AdoptionRequest
		if r == nil {
			response.Error = "adoption request required"
			return response
		}
		a, parseErr := address.Parse(r.Address)
		if parseErr != nil || a.Type != "sysbox_node" || a.Name != request.Target.Node || r.Topology != request.Target.Topology || r.TopologyUID != request.Target.Scope.TopologyUID || r.ResourceUID != request.Target.Scope.ResourceUID || r.Generation != request.Target.Scope.Generation {
			response.Error = "adoption target mismatch"
			return response
		}
	case "info", "start", "observe", "cancel", "renew", "output":
		if s.Dispatcher == nil {
			response.Error = "host task supervisor unavailable"
			return response
		}
	case "run-start", "run-observe", "run-renew", "run-cancel":
		r := request.RunRequest
		if r == nil || r.Topology != request.Target.Topology || r.TopologyUID != request.Target.Scope.TopologyUID || r.ResourceUID != request.Target.Scope.ResourceUID || r.Generation != request.Target.Scope.Generation {
			response.Error = "host run target mismatch"
			return response
		}
	}
	switch request.Operation {
	case "run-info":
		if s.Runs == nil || s.Runs.HostBootID == "" {
			err = fmt.Errorf("host run coordinator unavailable")
			break
		}
		response.BootID = s.Runs.HostBootID
	case "ssh-discover":
		if s.Dispatcher == nil {
			err = fmt.Errorf("host SSH discovery resolver unavailable")
			break
		}
		var result substrate.SSHAccessDiscovery
		result, err = s.Dispatcher.DiscoverSSHAccess(ctx, request.Target, request.SSHAccessID)
		if err == nil {
			response.SSHDiscovery = &result
		}
	case "gc-info":
		if s.GC == nil || s.GC.HostBootID == "" {
			err = fmt.Errorf("host GC coordinator unavailable")
			break
		}
		response.BootID = s.GC.HostBootID
	case "adoption-info":
		// This target does not exist in canonical state until adoption commits.
		// Return only the configured host boot; this read grants no authority.
		if s.Adoption == nil || s.Adoption.HostBootID == "" {
			err = fmt.Errorf("host adoption coordinator unavailable")
			break
		}
		response.BootID = s.Adoption.HostBootID
	case "file-info":
		if s.Files == nil {
			err = fmt.Errorf("host file supervisor unavailable")
			break
		}
		response.BootID, err = s.Files.BootID(ctx, request.Target)
	case "file-install":
		if s.Files == nil || request.FileRequest == nil || request.Grant == nil {
			err = fmt.Errorf("signed file request required")
			break
		}
		_, response.FileResult, err = s.Files.Install(ctx, request.Target, *request.FileRequest, *request.Grant)
	case "file-observe":
		if s.Files == nil {
			err = fmt.Errorf("host file supervisor unavailable")
			break
		}
		response.FileResult, err = s.Files.Observe(ctx, request.Target, request.FileHandle)
	case "file-inspect":
		if s.Files == nil {
			err = fmt.Errorf("host file supervisor unavailable")
			break
		}
		var result substrate.InstalledFileInspection
		result, err = s.Files.Inspect(ctx, request.Target, request.FileHandle)
		if err == nil {
			response.FileInspection = &result
			proof := request.FileHandle
			response.InspectedFile = &proof
		}
	case "file-cancel":
		if s.Files == nil {
			err = fmt.Errorf("host file supervisor unavailable")
			break
		}
		response.FileResult, err = s.Files.Cancel(ctx, request.Target, request.FileHandle)
	case "run-start":
		if s.Runs == nil || s.RunEffect == nil || request.RunRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host run coordinator unavailable")
			break
		}
		var observation RunObservation
		observation, err = s.Runs.StartAsync(ctx, serviceCtx, *request.RunRequest, *request.Grant, func(effectCtx context.Context) (json.RawMessage, error) {
			return s.RunEffect(effectCtx, *request.RunRequest)
		})
		response.RunObservation = &observation
	case "run-observe":
		if s.Runs == nil || request.RunRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host run coordinator unavailable")
			break
		}
		var observation RunObservation
		observation, err = s.Runs.ObserveAsync(ctx, *request.RunRequest, *request.Grant)
		response.RunObservation = &observation
	case "run-renew", "run-cancel":
		if s.Runs == nil || request.RunRequest == nil || request.Grant == nil {
			err = fmt.Errorf("signed host Run request required")
			break
		}
		if request.Operation == "run-renew" {
			err = s.Runs.Renew(ctx, *request.RunRequest, *request.Grant)
		} else {
			err = s.Runs.Cancel(ctx, *request.RunRequest, *request.Grant)
		}
	case "ssh-guest-mutate", "ssh-guest-observe":
		if s.SSHGuest == nil {
			err = fmt.Errorf("host guest SSH coordinator unavailable")
			break
		}
		var result substrate.SSHAccessResult
		var mutationErr error
		if request.Operation == "ssh-guest-observe" {
			result, mutationErr = s.SSHGuest.Observe(ctx, request.Target, *request.SSHMutation, *request.Grant)
		} else {
			result, mutationErr = s.SSHGuest.Mutate(ctx, request.Target, *request.SSHMutation, *request.Grant)
		}
		if mutationErr != nil {
			err = mutationErr
		} else {
			response.SSHEvidence, err = json.Marshal(result)
		}
	case "adoption-inspect":
		if s.Adoption == nil || s.AdoptionInspect == nil || request.AdoptionInspection == nil || request.Grant == nil {
			err = fmt.Errorf("adoption inspection unavailable")
			break
		}
		response.AdoptionEvidence, err = s.Adoption.Inspect(ctx, *request.AdoptionInspection, *request.Grant, func(ctx context.Context) (substrate.AdoptionEvidence, error) {
			return s.AdoptionInspect(ctx, *request.AdoptionInspection)
		})
	case "adoption-apply":
		if s.Adoption == nil || s.AdoptionApply == nil || request.AdoptionRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host adoption coordinator unavailable")
			break
		}
		response.AdoptionEvidence, err = s.Adoption.Apply(ctx, *request.AdoptionRequest, *request.Grant, func(ec context.Context) (json.RawMessage, error) {
			return s.AdoptionApply(ec, *request.AdoptionRequest, *request.Grant)
		})
	case "adoption-observe":
		if s.Adoption == nil || request.AdoptionRequest == nil || request.Grant == nil {
			err = fmt.Errorf("host adoption coordinator unavailable")
			break
		}
		response.AdoptionEvidence, err = s.Adoption.Observe(ctx, *request.AdoptionRequest, *request.Grant)
	case "gc-delete":
		if s.GC == nil || s.GCEffect == nil || request.GCCandidate == nil || request.Grant == nil {
			err = fmt.Errorf("host GC coordinator unavailable")
			break
		}
		if s.GCValidateTarget != nil {
			if err = s.GCValidateTarget(ctx, request.Target, *request.GCCandidate, *request.Grant); err != nil {
				break
			}
		}
		response.GCResult, err = s.GC.Delete(ctx, *request.GCCandidate, *request.Grant, func(effectCtx context.Context) (json.RawMessage, error) {
			if s.GCFencedEffect != nil {
				return s.GCFencedEffect(effectCtx, *request.GCCandidate, *request.Grant)
			}
			return s.GCEffect(effectCtx, *request.GCCandidate)
		})
	case "gc-observe":
		if s.GC == nil || request.GCCandidate == nil || request.Grant == nil {
			err = fmt.Errorf("host GC coordinator unavailable")
			break
		}
		response.GCResult, err = s.GC.Observe(ctx, *request.GCCandidate, *request.Grant)
	case "host-info":
		if s.Power == nil {
			err = fmt.Errorf("host power supervisor unavailable")
			break
		}
		response.BootID, err = s.Power.BootID(ctx, request.Target)
	case "power-apply":
		if s.Power == nil || request.Power == nil || request.Grant == nil {
			err = fmt.Errorf("signed power request required")
			break
		}
		err = s.Power.Execute(ctx, request.Target, *request.Power, *request.Grant)
	case "power-observe":
		if s.Power == nil || request.Grant == nil {
			err = fmt.Errorf("signed power proof required")
			break
		}
		err = s.Power.Observe(ctx, request.Target, *request.Grant)
	case "info":
		response.BootID, err = s.Dispatcher.BootID(ctx, request.Target)
	case "start":
		if request.Task == nil {
			err = guesttask.ErrUnauthorized
			break
		}
		record, err = s.Dispatcher.Start(ctx, request.Target, *request.Task)
		response.Handle = record.Handle
		response.Result = record.Result
	case "observe":
		record, err = s.Dispatcher.Observe(ctx, request.Target, request.Handle)
		response.Handle = record.Handle
		response.Result = record.Result
	case "cancel":
		record, err = s.Dispatcher.Cancel(ctx, request.Target, request.Handle)
		response.Result = record.Result
	case "renew":
		if request.Grant == nil {
			err = guesttask.ErrUnauthorized
			break
		}
		record, err = s.Dispatcher.Renew(ctx, request.Target, request.Handle, *request.Grant)
		response.Result = record.Result
	case "output":
		response.Output, err = s.Dispatcher.ReadOutput(ctx, request.Target, request.Handle, request.Stream, request.Cursor, request.Limit)
	default:
		err = fmt.Errorf("unknown supervisor operation")
	}
	if err != nil {
		response.Error = err.Error()
	}
	return response
}
