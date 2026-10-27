// Package windowstask implements the existing signed task contract for Windows.
// It separates durable admission from OS process control, allowing fault tests
// to exercise the same journal on every development platform.
package windowstask

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/taskprotocol"
)

var ErrConflict = errors.New("Windows task identity conflict")
var ErrUnauthorized = errors.New("Windows task grant rejected")
var ErrUnknown = errors.New("Windows task outcome unknown")
var taskID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

const Version = 1

type Platform interface {
	BootID() (string, error)
	Uptime() time.Duration
	Launch(context.Context, taskprotocol.Request) error
	Inspect(context.Context, taskprotocol.Record) (taskprotocol.Record, error)
	Stop(context.Context, taskprotocol.Record) (taskprotocol.Record, error)
}
type Engine struct {
	Root      string
	PublicKey ed25519.PublicKey
	Scope     controlplane.ExecutionScope
	Platform  Platform
}
type Call struct {
	Version      int                                       `json:"version"`
	Operation    string                                    `json:"operation"`
	Request      *taskprotocol.Request                     `json:"request,omitempty"`
	ID           string                                    `json:"id,omitempty"`
	Digest       string                                    `json:"digest,omitempty"`
	BootID       string                                    `json:"boot_id,omitempty"`
	Epoch        uint64                                    `json:"epoch,omitempty"`
	Scope        *controlplane.ExecutionScope              `json:"scope,omitempty"`
	Grant        *controlplane.SignedExecutionGrant        `json:"grant,omitempty"`
	Cancellation *controlplane.SignedExecutionCancellation `json:"cancellation,omitempty"`
	Stream       string                                    `json:"stream,omitempty"`
	Cursor       int64                                     `json:"cursor,omitempty"`
	Limit        int                                       `json:"limit,omitempty"`
}
type Response struct {
	Version    int                   `json:"version"`
	BootID     string                `json:"boot_id,omitempty"`
	Record     *taskprotocol.Record  `json:"record,omitempty"`
	Records    []taskprotocol.Record `json:"records,omitempty"`
	Output     []byte                `json:"output,omitempty"`
	NextCursor int64                 `json:"next_cursor,omitempty"`
	Error      string                `json:"error,omitempty"`
}
type scopeRecord struct {
	Version int    `json:"version"`
	Epoch   uint64 `json:"epoch"`
	Session string `json:"session"`
	Stopped bool   `json:"stopped"`
}

func (e *Engine) lock(ctx context.Context) (func(), error) {
	if e.Platform == nil || e.Root == "" || len(e.PublicKey) != ed25519.PublicKeySize || e.Scope.TopologyUID == "" || e.Scope.ResourceUID == "" || e.Scope.Generation < 1 {
		return nil, ErrUnauthorized
	}
	if err := os.MkdirAll(e.Root, 0700); err != nil {
		return nil, err
	}
	if checker, ok := e.Platform.(interface{ ValidateRoot(string) error }); ok {
		if err := checker.ValidateRoot(e.Root); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(e.Root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnauthorized
	}
	f := flock.New(filepath.Join(e.Root, "journal.lock"))
	ok, err := f.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !ok {
		f.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
func (e *Engine) path(id, name string) (string, error) {
	if !taskID.MatchString(id) {
		return "", ErrConflict
	}
	return filepath.Join(e.Root, id, name), nil
}
func readJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return ErrConflict
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return ErrConflict
	}
	return json.Unmarshal(raw, value)
}
func (e *Engine) load(id string) (taskprotocol.Record, error) {
	var r taskprotocol.Record
	path, err := e.path(id, "record.json")
	if err != nil {
		return r, err
	}
	if err = e.validateFile(path); err != nil {
		return r, err
	}
	if err = readJSON(path, &r); err != nil {
		return r, err
	}
	g := r.Grant.Grant
	if r.Version != taskprotocol.RecordVersion || r.ID != id || r.ID != g.ActionID || r.Digest != g.Digest || r.BootID != g.TargetBootID || g.Scope != e.Scope || r.DeadlineBootNS <= 0 {
		return r, ErrConflict
	}
	if r.Cancellation != nil {
		if controlplane.VerifyExecutionCancellation(e.PublicKey, *r.Cancellation) != nil || !sameGrant(g, r.Cancellation.Grant) {
			return r, ErrUnauthorized
		}
	} else if controlplane.VerifyExecutionGrant(e.PublicKey, r.Grant, r.BootID, r.Grant.IssuedAt) != nil {
		return r, ErrUnauthorized
	}
	return r, nil
}
func (e *Engine) save(r taskprotocol.Record) error {
	p, err := e.path(r.ID, "record.json")
	if err != nil {
		return err
	}
	return writeJSON(p, r)
}
func (e *Engine) scope() (scopeRecord, error) {
	var s scopeRecord
	err := e.validateFile(filepath.Join(e.Root, "scope.json"))
	if err == nil {
		err = readJSON(filepath.Join(e.Root, "scope.json"), &s)
	}
	if os.IsNotExist(err) {
		return scopeRecord{Version: 1}, nil
	}
	if err == nil && (s.Version != 1 || s.Epoch == 0 || s.Session == "") {
		err = ErrConflict
	}
	return s, err
}
func (e *Engine) verify(r taskprotocol.Request) (string, time.Duration, error) {
	if err := r.Validate(); err != nil {
		return "", 0, err
	}
	if r.UserID != 65534 || r.Args[0] == "sysbox-policy.v1" {
		return "", 0, ErrUnauthorized
	}
	for _, arg := range r.Args {
		if strings.ContainsRune(arg, 0) {
			return "", 0, ErrConflict
		}
	}
	for k, v := range r.Environment {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return "", 0, ErrConflict
		}
	}
	boot, err := e.Platform.BootID()
	if err != nil {
		return "", 0, err
	}
	g := r.Grant.Grant
	if g.Scope != e.Scope || g.Operation != "guest_task" || g.ActionID != r.ID || g.TargetBootID != boot || controlplane.VerifyExecutionGrant(e.PublicKey, r.Grant, boot, time.Now()) != nil {
		return "", 0, ErrUnauthorized
	}
	digest, err := r.Digest()
	if err != nil || digest != g.Digest {
		return "", 0, ErrUnauthorized
	}
	budget, err := controlplane.ExecutionGrantBudget(r.Grant, time.Now())
	return digest, budget, err
}
func (e *Engine) Start(ctx context.Context, request taskprotocol.Request) (taskprotocol.Record, error) {
	var zero taskprotocol.Record
	unlock, err := e.lock(ctx)
	if err != nil {
		return zero, err
	}
	digest, budget, err := e.verify(request)
	if err != nil {
		unlock()
		return zero, err
	}
	scope, err := e.scope()
	if err != nil {
		unlock()
		return zero, err
	}
	g := request.Grant.Grant
	if g.Epoch < scope.Epoch || g.Epoch == scope.Epoch && (scope.Stopped || scope.Session != "" && scope.Session != g.SessionID) {
		unlock()
		return zero, ErrUnauthorized
	}
	old, err := e.load(request.ID)
	if err == nil {
		unlock()
		if old.Digest != digest || !sameGrant(old.Grant.Grant, g) || old.CancelRequested {
			return zero, ErrConflict
		}
		return old, nil
	}
	if !os.IsNotExist(err) {
		unlock()
		return zero, err
	}
	records, err := e.list()
	if err != nil {
		unlock()
		return zero, err
	}
	for _, r := range records {
		observed, err := e.observe(ctx, r)
		if err != nil {
			unlock()
			return zero, err
		}
		if !observed.TerminationConfirmed {
			unlock()
			return zero, fmt.Errorf("%w: previous task remains in flight", ErrUnknown)
		}
	}
	scope = scopeRecord{Version: 1, Epoch: g.Epoch, Session: g.SessionID}
	if err = writeJSON(filepath.Join(e.Root, "scope.json"), scope); err != nil {
		unlock()
		return zero, err
	}
	r := taskprotocol.Record{Version: taskprotocol.RecordVersion, ID: request.ID, Digest: digest, Grant: request.Grant, BootID: request.Grant.BootID, DeadlineBootNS: int64(e.Platform.Uptime() + budget), Status: "prepared"}
	if err = e.save(r); err != nil {
		unlock()
		return r, err
	}
	unlock()
	// Only the caller that created prepared may launch. A duplicate observes
	// this intent, even if the initial helper disappears before acknowledgement.
	if err = e.Platform.Launch(ctx, request); err != nil {
		return r, err
	}
	return e.Observe(ctx, r.ID, r.Digest)
}
func (e *Engine) observe(ctx context.Context, r taskprotocol.Record) (taskprotocol.Record, error) {
	boot, err := e.Platform.BootID()
	if err != nil {
		return r, err
	}
	if r.TerminationConfirmed {
		return r, nil
	}
	if r.BootID != boot {
		if err := e.validateBootTransition(r.BootID, boot); err != nil {
			return r, err
		}
		r.Status = "unknown"
		r.ExitCodeKnown = false
		r.TerminationConfirmed = true
		r.Error = "guest boot changed; exit result unavailable"
		return r, e.save(r)
	}
	if r.Status == "prepared" && (r.CancelRequested || int64(e.Platform.Uptime()) >= r.DeadlineBootNS) {
		r.Status = "cancelled"
		r.CancelRequested = true
		r.TerminationConfirmed = true
		return r, e.save(r)
	}
	current, err := e.Platform.Inspect(ctx, r)
	if err != nil {
		return r, err
	}
	if current.TerminationConfirmed && !current.ExitCodeKnown {
		current.Status = "unknown"
	}
	if !reflect.DeepEqual(current, r) {
		return current, e.save(current)
	}
	return current, nil
}
func (e *Engine) Observe(ctx context.Context, id, digest string) (taskprotocol.Record, error) {
	unlock, err := e.lock(ctx)
	if err != nil {
		return taskprotocol.Record{}, err
	}
	defer unlock()
	r, err := e.load(id)
	if err != nil {
		return r, err
	}
	if r.Digest != digest {
		return r, ErrConflict
	}
	return e.observe(ctx, r)
}
func sameGrant(a, b controlplane.ExecutionGrant) bool {
	a.State = b.State
	a.ExpiresAt = b.ExpiresAt
	a.StopReceipt = b.StopReceipt
	return a == b
}
func (e *Engine) Cancel(ctx context.Context, call Call) (taskprotocol.Record, error) {
	var zero taskprotocol.Record
	unlock, err := e.lock(ctx)
	if err != nil {
		return zero, err
	}
	defer unlock()
	r, err := e.load(call.ID)
	if err != nil && !os.IsNotExist(err) {
		return r, err
	}
	if call.Cancellation != nil {
		g := call.Cancellation.Grant
		if controlplane.VerifyExecutionCancellation(e.PublicKey, *call.Cancellation) != nil || g.Scope != e.Scope || g.ActionID != call.ID || g.Digest != call.Digest {
			return zero, ErrUnauthorized
		}
		if os.IsNotExist(err) {
			r = taskprotocol.Record{Version: taskprotocol.RecordVersion, ID: call.ID, Digest: g.Digest, Grant: controlplane.SignedExecutionGrant{Grant: g, BootID: g.TargetBootID}, BootID: g.TargetBootID, DeadlineBootNS: int64(e.Platform.Uptime()) + 1, Status: "prepared", Cancellation: call.Cancellation}
		} else if !sameGrant(r.Grant.Grant, g) {
			return r, ErrConflict
		}
	} else if err != nil || call.Scope == nil || *call.Scope != r.Grant.Grant.Scope || call.Epoch != r.Grant.Grant.Epoch || call.BootID != r.BootID {
		return zero, ErrUnauthorized
	}
	if r.Digest != call.Digest {
		return r, ErrConflict
	}
	r.CancelRequested = true
	if err = e.save(r); err != nil {
		return r, err
	}
	scope, err := e.scope()
	if err != nil {
		return r, err
	}
	if r.Grant.Grant.Epoch >= scope.Epoch {
		scope = scopeRecord{Version: 1, Epoch: r.Grant.Grant.Epoch, Session: r.Grant.Grant.SessionID, Stopped: true}
		if err = writeJSON(filepath.Join(e.Root, "scope.json"), scope); err != nil {
			return r, err
		}
	}
	if r.TerminationConfirmed {
		return r, nil
	}
	boot, err := e.Platform.BootID()
	if err != nil {
		return r, err
	}
	if boot != r.BootID {
		if err := e.validateBootTransition(r.BootID, boot); err != nil {
			return r, err
		}
	}
	if r.Status == "prepared" || boot != r.BootID {
		r.Status = "cancelled"
		if boot != r.BootID {
			r.Status = "unknown"
		}
		r.TerminationConfirmed = true
		r.ExitCodeKnown = false
	} else {
		r, err = e.Platform.Stop(ctx, r)
		if err != nil {
			return r, err
		}
	}
	return r, e.save(r)
}
func (e *Engine) Renew(ctx context.Context, id, digest string, grant controlplane.SignedExecutionGrant) (taskprotocol.Record, error) {
	unlock, err := e.lock(ctx)
	if err != nil {
		return taskprotocol.Record{}, err
	}
	defer unlock()
	r, err := e.load(id)
	if err != nil {
		return r, err
	}
	boot, err := e.Platform.BootID()
	if err != nil {
		return r, err
	}
	if r.Digest != digest || r.TerminationConfirmed || r.CancelRequested || int64(e.Platform.Uptime()) >= r.DeadlineBootNS || !sameGrant(r.Grant.Grant, grant.Grant) || controlplane.VerifyExecutionGrant(e.PublicKey, grant, boot, time.Now()) != nil || grant.Grant.ExpiresAt.Before(r.Grant.Grant.ExpiresAt) {
		return r, ErrUnauthorized
	}
	if grant.Grant.ExpiresAt.After(r.Grant.Grant.ExpiresAt) {
		budget, err := controlplane.ExecutionGrantBudget(grant, time.Now())
		if err != nil {
			return r, err
		}
		r.DeadlineBootNS = int64(e.Platform.Uptime() + budget)
	}
	r.Grant = grant
	return r, e.save(r)
}
func (e *Engine) list() ([]taskprotocol.Record, error) {
	entries, err := os.ReadDir(e.Root)
	if err != nil {
		return nil, err
	}
	var records []taskprotocol.Record
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		r, err := e.load(entry.Name())
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}
func (e *Engine) Handle(ctx context.Context, call Call) Response {
	response := Response{Version: Version}
	if call.Version != Version {
		response.Error = "unsupported task protocol"
		return response
	}
	var err error
	switch call.Operation {
	case "info":
		response.BootID, err = e.Platform.BootID()
	case "start":
		if call.Request == nil {
			err = ErrConflict
		} else {
			var r taskprotocol.Record
			r, err = e.Start(ctx, *call.Request)
			response.Record = &r
		}
	case "observe":
		var r taskprotocol.Record
		r, err = e.Observe(ctx, call.ID, call.Digest)
		response.Record = &r
	case "cancel":
		var r taskprotocol.Record
		r, err = e.Cancel(ctx, call)
		response.Record = &r
	case "renew":
		if call.Grant == nil {
			err = ErrConflict
		} else {
			var r taskprotocol.Record
			r, err = e.Renew(ctx, call.ID, call.Digest, *call.Grant)
			response.Record = &r
		}
	case "list":
		var unlock func()
		unlock, err = e.lock(ctx)
		if err == nil {
			response.Records, err = e.list()
			unlock()
		}
	case "output":
		response.Output, response.NextCursor, err = e.output(ctx, call)
	default:
		err = fmt.Errorf("Windows task operation %q unsupported", call.Operation)
	}
	if err != nil {
		response.Error = err.Error()
	}
	return response
}
func (e *Engine) output(ctx context.Context, call Call) ([]byte, int64, error) {
	if call.Stream != "stdout" && call.Stream != "stderr" || call.Cursor < 0 || call.Limit < 1 || call.Limit > 64<<10 {
		return nil, 0, ErrConflict
	}
	unlock, err := e.lock(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer unlock()
	r, err := e.load(call.ID)
	if err != nil {
		return nil, 0, err
	}
	if r.Digest != call.Digest {
		return nil, 0, ErrConflict
	}
	path, err := e.path(call.ID, call.Stream)
	if err != nil {
		return nil, 0, err
	}
	if err = e.validateFile(path); err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	data := make([]byte, call.Limit)
	n, err := f.ReadAt(data, call.Cursor)
	if err == io.EOF {
		err = nil
	}
	return data[:n], call.Cursor + int64(n), err
}

func (e *Engine) validateFile(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	if checker, ok := e.Platform.(interface{ ValidateFile(string) error }); ok {
		return checker.ValidateFile(path)
	}
	return nil
}

// checkLaunchDeadline runs under the journal lock immediately before each OS
// launch boundary. Preparation time must not extend the admitted authority.
func (e *Engine) checkLaunchDeadline(ctx context.Context, r taskprotocol.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.CancelRequested || int64(e.Platform.Uptime()) >= r.DeadlineBootNS {
		return ErrUnauthorized
	}
	return nil
}

func (e *Engine) validateBootTransition(old, current string) error {
	if platform, ok := e.Platform.(interface{ ValidBootID(string) bool }); ok {
		if !platform.ValidBootID(old) || !platform.ValidBootID(current) {
			return fmt.Errorf("%w: incompatible boot identity requires independent fencing evidence", ErrUnknown)
		}
	}
	return nil
}
