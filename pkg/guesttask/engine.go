package guesttask

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"golang.org/x/sys/unix"
)

// Engine is a guest-local authority. Root and CgroupRoot must be private,
// administrator-owned directories; guest tasks cannot change their contents.
// Every task gets a detached supervisor, independent of the RPC server.
type Engine struct {
	Root         string
	CgroupRoot   string
	PublicKey    ed25519.PublicKey
	WorkerBinary string
	WorkerArgs   []string
}

func BootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(raw)), err
}
func (e *Engine) taskPath(id string) string { return filepath.Join(e.Root, id) }
func (e *Engine) lock(ctx context.Context, id string) (func(), error) {
	if !validID.MatchString(id) {
		return nil, ErrConflict
	}
	if err := os.MkdirAll(e.Root, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(e.Root, id+".lock"))
	ok, err := lock.TryLockContext(ctx, 5*time.Millisecond)
	if err != nil || !ok {
		lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, ctx.Err()
	}
	return func() { lock.Close() }, nil
}
func atomicJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > MaxInput*2 {
		return fmt.Errorf("oversized task record")
	}
	return json.Unmarshal(raw, value)
}
func (e *Engine) load(id string) (Record, error) {
	var record Record
	err := readJSON(filepath.Join(e.taskPath(id), "record.json"), &record)
	if err == nil && (record.Version != Version || record.ID != id || record.Digest == "" || record.BootID == "" || record.DeadlineBootNS <= 0) {
		err = fmt.Errorf("task record requires explicit rebuild")
	}
	return record, err
}
func (e *Engine) save(r Record) error {
	return atomicJSON(filepath.Join(e.taskPath(r.ID), "record.json"), r)
}

func (e *Engine) Start(ctx context.Context, req Request) (Record, error) {
	if err := req.Validate(); err != nil {
		return Record{}, err
	}
	boot, err := BootID()
	if err != nil {
		return Record{}, err
	}
	if err = verifyGrant(req.Grant, e.PublicKey, boot, time.Now()); err != nil {
		return Record{}, err
	}
	digest, err := req.Digest()
	if err != nil {
		return Record{}, err
	}
	if digest != req.Grant.Grant.Digest {
		return Record{}, ErrUnauthorized
	}
	// The scope lock serializes epoch retirement with all task admissions.
	scope := fmt.Sprintf("scope-%x", sha256.Sum256([]byte(req.Grant.Grant.Scope.TopologyUID+"\x00"+req.Grant.Grant.Scope.ResourceUID)))
	scopes := Engine{Root: filepath.Join(e.Root, ".scopes")}
	unlockScope, err := scopes.lock(ctx, scope)
	if err != nil {
		return Record{}, err
	}
	defer unlockScope()
	var epoch struct {
		Epoch   uint64 `json:"epoch"`
		Session string `json:"session"`
	}
	epochPath := filepath.Join(scopes.Root, scope+".json")
	err = readJSON(epochPath, &epoch)
	if err != nil && !os.IsNotExist(err) {
		return Record{}, err
	}
	if req.Grant.Grant.Epoch < epoch.Epoch || req.Grant.Grant.Epoch == epoch.Epoch && epoch.Session != "" && epoch.Session != req.Grant.Grant.SessionID {
		return Record{}, ErrUnauthorized
	}
	unlock, err := e.lock(ctx, req.ID)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	old, err := e.load(req.ID)
	if err == nil {
		if old.Digest != digest || old.Grant.Grant.Epoch != req.Grant.Grant.Epoch || old.Grant.Grant.SessionID != req.Grant.Grant.SessionID {
			return Record{}, ErrConflict
		}
		return old, nil
	}
	if !os.IsNotExist(err) {
		return Record{}, err
	}
	if req.Grant.Grant.Epoch > epoch.Epoch {
		records, err := e.listUnlocked()
		if err != nil {
			return Record{}, err
		}
		for _, r := range records {
			if r.Grant.Grant.Scope.TopologyUID == req.Grant.Grant.Scope.TopologyUID && r.Grant.Grant.Scope.ResourceUID == req.Grant.Grant.Scope.ResourceUID && !r.TerminationConfirmed {
				return Record{}, fmt.Errorf("previous task must stop before epoch takeover")
			}
		}
	}
	if err = e.verifyCgroupRoot(); err != nil {
		return Record{}, err
	}
	if e.WorkerBinary == "" {
		return Record{}, fmt.Errorf("task supervisor executable unavailable")
	}
	epoch.Epoch, epoch.Session = req.Grant.Grant.Epoch, req.Grant.Grant.SessionID
	if err = atomicJSON(epochPath, epoch); err != nil {
		return Record{}, err
	}
	record := Record{Version: Version, ID: req.ID, Digest: digest, Grant: req.Grant, BootID: boot, Status: "prepared"}
	budget, err := controlplane.ExecutionGrantBudget(req.Grant, time.Now())
	if err != nil {
		return Record{}, err
	}
	bootNow, err := bootNanos()
	if err != nil {
		return Record{}, err
	}
	record.DeadlineBootNS = bootNow + int64(budget)
	// Sensitive argv/env/stdin live only in a sealed anonymous memory file.
	// The durable record contains identity and digest, never request plaintext.
	payload, err := json.Marshal(req)
	if err != nil {
		return Record{}, err
	}
	fd, err := unix.MemfdCreate("sysbox-task-request", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return Record{}, err
	}
	input := os.NewFile(uintptr(fd), "task-request")
	defer input.Close()
	if _, err = input.Write(payload); err != nil {
		return Record{}, err
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return Record{}, err
	}
	if _, err = unix.FcntlInt(input.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
		return Record{}, err
	}
	if err = e.save(record); err != nil {
		return Record{}, err
	}
	// A persisted prepared record is never relaunched. Losing this acknowledgement
	// is resolved through Observe; an ambiguous spawn cannot replay user code.
	args := append([]string{}, e.WorkerArgs...)
	if len(args) == 0 {
		args = []string{"--guest-task-worker"}
	}
	cmd := exec.Command(e.WorkerBinary, args...)
	cmd.ExtraFiles = []*os.File{input}
	cmd.Env = append(os.Environ(), "SYSBOX_GUESTTASK_WORKER=1", "SYSBOX_GUESTTASK_ROOT="+e.Root, "SYSBOX_GUESTTASK_CGROUP="+e.CgroupRoot, "SYSBOX_GUESTTASK_ID="+req.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		record.Status = "unknown"
		record.Error = err.Error()
		_ = e.save(record)
		return record, err
	}
	// Reap the helper when this RPC process survives. Its stdio and process
	// lifetime are independent of this goroutine or the transport connection.
	go cmd.Wait()
	return record, nil
}

func (e *Engine) verifyCgroupRoot() error {
	if e.CgroupRoot == "" {
		return fmt.Errorf("cgroup-v2 task delegation required")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(e.CgroupRoot, &stat); err != nil {
		return err
	}
	if stat.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("task isolation requires cgroup v2")
	}
	if _, err := os.Stat(filepath.Join(e.CgroupRoot, "cgroup.procs")); err != nil {
		return err
	}
	return nil
}
func (e *Engine) group(id string) string { return filepath.Join(e.CgroupRoot, "sysbox-task-"+id) }
func groupInode(path string) (uint64, error) {
	var s unix.Stat_t
	if err := unix.Stat(path, &s); err != nil {
		return 0, err
	}
	return s.Ino, nil
}
func (e *Engine) openGroup(r Record) (*os.File, error) {
	f, err := os.Open(e.group(r.ID))
	if err != nil {
		return nil, err
	}
	var s unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &s); err != nil || r.CgroupInode == 0 || s.Ino != r.CgroupInode {
		f.Close()
		return nil, ErrConflict
	}
	return f, nil
}
func groupFile(group *os.File, name string, flag int) (*os.File, error) {
	fd, err := unix.Openat(int(group.Fd()), name, flag|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func groupEmpty(group *os.File) (bool, error) {
	f, err := groupFile(group, "cgroup.events", unix.O_RDONLY)
	if err != nil {
		return false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "populated 0" {
			return true, nil
		}
		if line == "populated 1" {
			return false, nil
		}
	}
	return false, ErrUnknown
}
func killGroup(group *os.File) error {
	f, err := groupFile(group, "cgroup.kill", unix.O_WRONLY)
	if err != nil {
		if err == unix.ENOENT {
			return freezeAndKill(group)
		}
		return err
	}
	defer f.Close()
	_, err = f.WriteString("1")
	return err
}

// Linux 5.10 provides cgroup v2 freeze and pidfds but predates cgroup.kill.
// Freeze the whole subtree first, then pin each member before SIGKILL. Never
// signal a numeric PID and never thaw a partially retired subtree on error.
func freezeAndKill(group *os.File) error {
	freeze, err := groupFile(group, "cgroup.freeze", unix.O_WRONLY)
	if err != nil {
		return err
	}
	_, err = freeze.WriteString("1")
	freeze.Close()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	frozen := false
	for time.Now().Before(deadline) {
		f, err := groupFile(group, "cgroup.events", unix.O_RDONLY)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), "frozen 1\n") {
			frozen = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !frozen {
		return fmt.Errorf("task cgroup could not be frozen")
	}
	return killFrozenMembers(group)
}
func killFrozenMembers(group *os.File) error {
	// Visit child cgroups as well: freezing a parent freezes all descendants.
	entries, err := os.ReadDir(fmt.Sprintf("/proc/self/fd/%d", group.Fd()))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fd, err := unix.Openat(int(group.Fd()), entry.Name(), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		child := os.NewFile(uintptr(fd), entry.Name())
		err = killFrozenMembers(child)
		child.Close()
		if err != nil {
			return err
		}
	}
	f, err := groupFile(group, "cgroup.procs", unix.O_RDONLY)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		return err
	}
	for _, word := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(word)
		if err != nil || pid <= 0 {
			return ErrUnknown
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err == unix.ESRCH {
			continue
		}
		if err != nil {
			return err
		}
		// Re-read current membership after pinning: PID reuse outside this
		// frozen cgroup cannot turn an unrelated process into a kill target.
		members, err := groupFile(group, "cgroup.procs", unix.O_RDONLY)
		if err != nil {
			unix.Close(fd)
			return err
		}
		current, err := io.ReadAll(members)
		members.Close()
		if err != nil {
			unix.Close(fd)
			return err
		}
		present := false
		for _, member := range strings.Fields(string(current)) {
			if member == word {
				present = true
				break
			}
		}
		if present {
			err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		}
		unix.Close(fd)
		if err != nil && err != unix.ESRCH {
			return err
		}
	}
	return nil
}

func (e *Engine) Observe(ctx context.Context, id, digest string) (Record, error) {
	unlock, err := e.lock(ctx, id)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	r, err := e.load(id)
	if err != nil {
		return r, err
	}
	if r.Digest != digest {
		return r, ErrConflict
	}
	boot, err := BootID()
	if err != nil {
		return r, err
	}
	if r.BootID != boot {
		r.Status = "unknown"
		r.ExitCodeKnown = false
		r.TerminationConfirmed = true
		r.Error = "guest boot changed; exit result unavailable"
		return r, e.save(r)
	}
	if r.TerminationConfirmed {
		return r, nil
	}
	if r.CgroupInode == 0 {
		if r.Status == "prepared" && remainingRecord(r) <= 0 {
			r.Status = "cancelled"
			r.CancelRequested = true
			r.TerminationConfirmed = true
			return r, e.save(r)
		}
		return r, nil
	}
	group, err := e.openGroup(r)
	if err != nil {
		return r, err
	}
	defer group.Close()
	empty, err := groupEmpty(group)
	if err != nil {
		return r, err
	}
	if empty && r.Status == "running" { // Supervisor may still be committing the exit result.
		r.Status = "unknown"
		r.Error = "processes absent; supervisor result not yet available"
	}
	if r.SupervisorPID > 0 {
		start, probeErr := processStart(r.SupervisorPID)
		if os.IsNotExist(probeErr) || probeErr == nil && start != r.SupervisorStartTicks {
			if err = killGroup(group); err != nil {
				return r, err
			}
			r.TerminationConfirmed, err = groupEmpty(group)
			if err != nil {
				return r, err
			}
			r.Status = "unknown"
			r.ExitCodeKnown = false
			r.Error = "supervisor identity disappeared; task retired"
			return r, e.save(r)
		}
		if probeErr != nil {
			return r, probeErr
		}
	}
	return r, nil
}
func (e *Engine) Cancel(ctx context.Context, id, digest string) error {
	return e.cancel(ctx, id, digest, nil)
}
func (e *Engine) CancelWithProof(ctx context.Context, call Call) error {
	return e.cancel(ctx, call.ID, call.Digest, &call)
}
func (e *Engine) cancel(ctx context.Context, id, digest string, proof *Call) error {
	unlock, err := e.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	r, err := e.load(id)
	if err != nil {
		return err
	}
	if r.Digest != digest {
		return ErrConflict
	}
	if proof != nil && (proof.BootID != r.BootID || proof.Epoch != r.Grant.Grant.Epoch || proof.Scope == nil || *proof.Scope != r.Grant.Grant.Scope) {
		return ErrConflict
	}
	r.CancelRequested = true
	if err = e.save(r); err != nil {
		return err
	}
	boot, err := BootID()
	if err != nil {
		return err
	}
	if r.BootID != boot || r.TerminationConfirmed {
		return nil
	}
	if r.CgroupInode == 0 {
		return nil
	}
	group, err := e.openGroup(r)
	if err != nil {
		return err
	}
	defer group.Close()
	return killGroup(group)
}
func (e *Engine) listUnlocked() ([]Record, error) {
	entries, err := os.ReadDir(e.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		r, err := e.load(entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func (e *Engine) List(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return e.listUnlocked()
}
func (e *Engine) ReadOutput(ctx context.Context, id, digest, stream string, cursor int64, limit int) ([]byte, int64, error) {
	if stream != "stdout" && stream != "stderr" || cursor < 0 || limit < 1 || limit > 64<<10 {
		return nil, cursor, ErrConflict
	}
	r, err := e.Observe(ctx, id, digest)
	if err != nil {
		return nil, cursor, err
	}
	_ = r
	f, err := os.Open(filepath.Join(e.taskPath(id), stream))
	if os.IsNotExist(err) {
		return nil, cursor, nil
	}
	if err != nil {
		return nil, cursor, err
	}
	defer f.Close()
	out := make([]byte, limit)
	n, err := f.ReadAt(out, cursor)
	if err == io.EOF {
		err = nil
	}
	return out[:n], cursor + int64(n), err
}
func processStart(pid int) (uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	i := strings.LastIndex(string(raw), ")")
	if i < 0 {
		return 0, ErrUnknown
	}
	fields := strings.Fields(string(raw[i+1:]))
	if len(fields) < 20 {
		return 0, ErrUnknown
	}
	return strconv.ParseUint(fields[19], 10, 64)
}
