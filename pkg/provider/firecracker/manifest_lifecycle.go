package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) lockedManifest(ctx context.Context, h substrate.NodeHandle) (*vmProcess, *vmManifest, func(), error) {
	dir, err := s.manifestDirectory(h)
	if err != nil {
		return nil, nil, nil, err
	}
	vm := lookupOrReserveVM(dir)
	if err := vm.lockOperation(ctx); err != nil {
		return nil, nil, nil, err
	}
	unlock, err := s.lockHostOperation(ctx, h)
	if err != nil {
		vm.unlockOperation()
		return nil, nil, nil, err
	}
	release := func() { unlock(); vm.unlockOperation() }
	m, err := s.loadManifest(ctx, h)
	if err != nil {
		release()
		return nil, nil, nil, err
	}
	return vm, m, release, nil
}

func manifestHandle(m *vmManifest) substrate.NodeHandle {
	h := firecrackerNodeHandle(m.VMID, cloneHandleState(&m.Handle))
	h.Generation = m.Generation
	return h
}

// recoverLaunch probes complete argv and executable identity. A starting record
// without an anchored process is uncertain unless exactly one socket-backed
// candidate exists. It is never an authorization to launch a second child.
func recoverLaunch(m *vmManifest) error {
	if m.Phase == "attaching" {
		return fmt.Errorf("firecracker unresolved attachment intent; generation retained for inspection")
	}
	if m.Phase != "starting" || m.Process.PID > 0 {
		return nil
	}
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	if m.Process.BootID != boot {
		return fmt.Errorf("firecracker unresolved launch intent: host boot identity conflict")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	var candidates []processIdentity
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		p, err := captureProcessIdentity(pid)
		if err != nil {
			continue
		}
		if p.Executable == m.Process.Executable && reflect.DeepEqual(p.Argv, m.Process.Argv) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) != 1 {
		return fmt.Errorf("firecracker unresolved launch intent: %d exact candidates", len(candidates))
	}
	st, err := os.Lstat(m.Handle.Socket)
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("firecracker unresolved launch intent: socket evidence missing")
	}
	owns, err := processOwnsSocket(candidates[0].PID, m.Handle.Socket)
	if err != nil {
		return err
	}
	if !owns {
		return fmt.Errorf("firecracker unresolved launch intent: candidate does not own socket")
	}
	m.Process = candidates[0]
	m.Phase = "running"
	m.Handle.PID = m.Process.PID
	m.Handle.PIDStart = m.Process.StartTime
	return saveManifest(m)
}

func (s *Substrate) startManifestNode(ctx context.Context, h substrate.NodeHandle) error {
	vm, m, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return err
	}
	defer release()
	if vm.snapshot().destroying || vm.snapshot().destroyed {
		return fmt.Errorf("firecracker VM is being destroyed")
	}
	if err := recoverLaunch(m); err != nil {
		return err
	}
	live, _, err := probeProcess(m.Process)
	if err != nil {
		return err
	}
	if live {
		return nil
	}
	if m.Phase == "starting" {
		return fmt.Errorf("firecracker launch result unknown")
	}
	unlockTargets, err := lockStartAttachments(ctx, m)
	if err != nil {
		return err
	}
	defer unlockTargets()
	// An adopted process has no local reaper. Fresh absence, not cached started,
	// authorizes a subsequent start of this same retained generation.
	vm.mu.Lock()
	vm.started = false
	vm.mu.Unlock()
	if !vm.reserveStart() {
		return fmt.Errorf("firecracker start already in progress")
	}
	defer vm.releaseStart()
	executable, err := exec.LookPath(s.firecrackerBin)
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	args := []string{"--config-file", m.Handle.ConfigPath, "--api-sock", m.Handle.Socket}
	var cmd *exec.Cmd
	if m.Handle.NetnsName != "" {
		cmd = exec.Command(ipBin, append([]string{"netns", "exec", m.Handle.NetnsName, executable}, args...)...)
	} else {
		cmd = exec.Command(executable, args...)
	}
	// Short socket parents may live on ephemeral storage and disappear at
	// reboot. Recreate only validated owner-controlled directories before intent.
	for _, path := range []string{m.Handle.Socket, m.Handle.VsockUDS} {
		if path != "" {
			if err := ensureOwnedSocketDirectory(path); err != nil {
				return err
			}
		}
	}
	boot, err := currentBootID()
	if err != nil {
		return err
	}
	m.Phase = "starting"
	m.Process = processIdentity{Executable: executable, Argv: append([]string{executable}, args...), BootID: boot}
	m.Handle.Ready = false
	if op, ok := substrate.OperationIdentityFromContext(ctx); ok {
		m.ActionID = op.ActionID
	}
	if err := saveManifest(m); err != nil {
		return err
	}
	// These sockets were validated as controlled sockets and the old anchor was
	// proved exited before removing them. A launch intent has already committed.
	for _, path := range []string{m.Handle.Socket, m.Handle.VsockUDS} {
		if path != "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	logPath := filepath.Join(m.Handle.VMDir, "firecracker.log")
	if err := rejectSymlinks(logPath); err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := ctx.Err(); err != nil {
		log.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		log.Close()
		m.Phase = "prepared"
		m.Process = processIdentity{}
		return errors.Join(fmt.Errorf("start firecracker: %w", err), saveManifest(m))
	}
	log.Close()
	startTime := processStartTime(cmd.Process.Pid)
	vm.mu.Lock()
	vm.cmd = cmd
	vm.pid = cmd.Process.Pid
	vm.startTime = startTime
	vm.started = true
	vm.starting = true
	vm.exitCode = nil
	vm.exitError = ""
	vm.done = make(chan struct{})
	vm.socket = m.Handle.Socket
	vm.cfgPath = m.Handle.ConfigPath
	vm.netnsName = m.Handle.NetnsName
	vm.mu.Unlock()
	go reapVMProcess(vm, cmd)
	m.Process.PID = cmd.Process.Pid
	m.Process.StartTime = startTime
	if err := saveManifest(m); err != nil {
		return err
	}
	// A launcher may exec Firecracker after cmd.Start returns. Wait briefly for
	// the process identity to settle before durably publishing its full anchor.
	var anchor processIdentity
	for attempts := 0; attempts < 100; attempts++ {
		p, e := captureProcessIdentity(cmd.Process.Pid)
		if e == nil && p.StartTime == startTime {
			if reflect.DeepEqual(p, anchor) {
				break
			}
			anchor = p
		}
		if processReaped(vm) {
			return fmt.Errorf("firecracker exited before anchor commit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if anchor.PID == 0 {
		return fmt.Errorf("firecracker launched but process identity unavailable; retaining launch intent")
	}
	m.Process = anchor
	m.Phase = "running"
	m.Handle.PID = anchor.PID
	m.Handle.PIDStart = anchor.StartTime
	if err := saveManifest(m); err != nil {
		return err
	}
	m.Handle.Ready = waitFirecrackerBoot(ctx, &m.Handle, 5*time.Second) == nil && !processReaped(vm)
	if err := saveManifest(m); err != nil {
		return err
	}
	if hs, _ := h.Provider.(*HandleState); hs != nil {
		hs.PID, hs.PIDStart, hs.Ready = m.Handle.PID, m.Handle.PIDStart, m.Handle.Ready
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if processReaped(vm) {
		return fmt.Errorf("firecracker exited during startup")
	}
	return nil
}

func (s *Substrate) observeManifest(ctx context.Context, h substrate.NodeHandle, m *vmManifest, state vmProcessSnapshot) (substrate.NodeObservation, error) {
	obs := substrate.NodeObservation{Exists: true, ExternalID: h.ID, OwnerUID: m.OwnerUID, ResourceUID: m.ResourceUID, Generation: m.Generation, LastSeen: time.Now().UTC(), Status: substrate.NodeStatusUnknown}
	if err := recoverLaunch(m); err != nil {
		obs.Reason = err.Error()
		return obs, err
	}
	live, paused, err := probeProcess(m.Process)
	if err != nil {
		obs.Reason = err.Error()
		return obs, err
	}
	obs.PID = m.Process.PID
	obs.Running = live
	if !live {
		obs.Status = substrate.NodeStatusExited
		obs.Reason = "verified process absent; durable generation retained"
		if state.pid == m.Process.PID {
			obs.ExitCode = state.exitCode
		}
		return obs, nil
	}
	obs.Status = substrate.NodeStatusRunning
	obs.Reason = "verified process alive; readiness unconfirmed"
	if paused {
		obs.Status = substrate.NodeStatusPaused
		obs.Reason = "verified process stopped"
		return obs, nil
	}
	st, err := os.Lstat(m.Handle.Socket)
	if err == nil && st.Mode()&os.ModeSocket != 0 {
		// Readiness is probed for each observation; persisted Ready is never trusted.
		obs.Healthy = m.Handle.VsockUDS == "" || waitFirecrackerBoot(ctx, &m.Handle, 250*time.Millisecond) == nil
	} else if err != nil && !os.IsNotExist(err) {
		obs.Status = substrate.NodeStatusUnknown
		return obs, err
	}
	if obs.Healthy {
		obs.Reason = "verified process alive and endpoint ready"
	}
	return obs, nil
}

func (s *Substrate) observeManifestNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	vm, m, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		if errors.Is(err, errGenerationAbsent) {
			return substrate.NodeObservation{ExternalID: h.ID, Status: substrate.NodeStatusMissing, LastSeen: time.Now().UTC()}, nil
		}
		return substrate.NodeObservation{ExternalID: h.ID, Status: substrate.NodeStatusUnknown, Reason: err.Error()}, err
	}
	defer release()
	return s.observeManifest(ctx, h, m, vm.snapshot())
}

func (s *Substrate) adoptManifestNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeHandle, error) {
	vm, m, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return h, err
	}
	defer release()
	obs, err := s.observeManifest(ctx, h, m, vm.snapshot())
	if err != nil {
		return h, err
	}
	if !obs.Running {
		return h, fmt.Errorf("firecracker adopt: verified process not running")
	}
	vm.mu.Lock()
	vm.pid = m.Process.PID
	vm.startTime = m.Process.StartTime
	vm.socket = m.Handle.Socket
	vm.cfgPath = m.Handle.ConfigPath
	vm.netnsName = m.Handle.NetnsName
	vm.started = true
	vm.mu.Unlock()
	recovered := manifestHandle(m)
	recovered.Net = h.Net
	if h.Conn.Kind != "" {
		recovered.Conn = h.Conn
	}
	if supplied, _ := h.Provider.(*HandleState); supplied != nil {
		recovered.Provider.(*HandleState).SSHPass = supplied.SSHPass
	}
	return recovered, nil
}

func (s *Substrate) signalManifestNode(ctx context.Context, h substrate.NodeHandle, signal syscall.Signal) error {
	_, m, release, err := s.lockedManifest(ctx, h)
	if errors.Is(err, errGenerationAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	if err := recoverLaunch(m); err != nil {
		return err
	}
	_, paused, err := probeProcess(m.Process)
	if err != nil {
		return err
	}
	continueOnStop := signal == syscall.SIGTERM && (paused || m.Phase == "paused")
	if err := signalManifestProcess(m, signal); err != nil {
		return err
	}
	// A stopped Linux process cannot consume SIGTERM until it is continued.
	// Both signals bind the same validated durable process identity.
	if continueOnStop {
		if err := signalManifestProcess(m, syscall.SIGCONT); err != nil {
			return err
		}
	}
	switch signal {
	case syscall.SIGSTOP:
		m.Phase = "paused"
	case syscall.SIGCONT:
		m.Phase = "running"
	case syscall.SIGTERM:
		m.Phase = "stopping"
	}
	return saveManifest(m)
}

func (s *Substrate) destroyManifestNode(ctx context.Context, h substrate.NodeHandle) error {
	vm, m, release, err := s.lockedManifest(ctx, h)
	if errors.Is(err, errGenerationAbsent) {
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	if err := recoverLaunch(m); err != nil {
		return err
	}
	if err := signalManifestProcess(m, syscall.SIGKILL); err != nil {
		return err
	}
	vm.mu.Lock()
	vm.destroying = true
	vm.mu.Unlock()
	state := vm.snapshot()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		live, _, err := probeProcess(m.Process)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		reaped := true
		if state.done != nil {
			select {
			case <-state.done:
			default:
				reaped = false
			}
		}
		if err == nil && !live && reaped {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("firecracker process exit not confirmed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Revalidate controlled paths after waiting, before deletion.
	if _, err := s.loadManifest(ctx, h); err != nil {
		return err
	}
	if err := s.recordStoppedDeletion(ctx, h, m); err != nil {
		return err
	}
	for _, path := range []string{m.Handle.Socket, m.Handle.VsockUDS} {
		if path != "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if err := os.RemoveAll(m.Handle.VMDir); err != nil {
		return err
	}
	vm.mu.Lock()
	vm.destroyed = true
	vm.mu.Unlock()
	vmMu.Lock()
	if vmStore[m.Handle.VMDir] == vm {
		delete(vmStore, m.Handle.VMDir)
	}
	vmMu.Unlock()
	return nil
}

// DiscoverManagedNode is recovery for system-owned generations, not external import.
func (s *Substrate) DiscoverManagedNode(ctx context.Context, identity substrate.OperationIdentity, name string) (substrate.NodeHandle, error) {
	if !validIdentityPart(identity.OwnerUID) || !validIdentityPart(identity.ResourceUID) || !validIdentityPart(identity.TopologyUID) || identity.Generation <= 0 {
		return substrate.NodeHandle{}, fmt.Errorf("invalid managed discovery identity")
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	dir := filepath.Join(root, identity.TopologyUID, identity.ResourceUID, strconv.FormatInt(identity.Generation, 10))
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		handles, e := s.ListManagedNodes(ctx)
		if e != nil {
			return substrate.NodeHandle{}, e
		}
		for _, h := range handles {
			hs, _ := h.Provider.(*HandleState)
			if hs != nil && hs.OwnerUID == identity.OwnerUID && hs.TopologyUID == identity.TopologyUID && hs.ResourceUID == identity.ResourceUID && hs.Generation == identity.Generation {
				dir = hs.VMDir
				break
			}
		}
	}
	// VM ID is stored inside the controlled generation, and validated independently.
	raw, err := readRegularFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			if _, e := os.Lstat(dir); os.IsNotExist(e) {
				if residue, probeErr := hasLaunchResidue(filepath.Join(dir, "vm_config.json")); probeErr != nil {
					return substrate.NodeHandle{}, probeErr
				} else if residue {
					return substrate.NodeHandle{}, fmt.Errorf("firecracker managed generation has orphan process residue")
				}
				return substrate.NodeHandle{}, driver.Wrap(driver.ErrorNotFound, "firecracker", "managed generation absent", nil)
			}
		}
		return substrate.NodeHandle{}, fmt.Errorf("managed generation manifest unknown: %w", err)
	}
	var m vmManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return substrate.NodeHandle{}, err
	}
	h := manifestHandle(&m)
	ctx = substrate.WithOperationIdentity(ctx, identity)
	_, verified, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	defer release()
	if err := recoverLaunch(verified); err != nil {
		return substrate.NodeHandle{}, err
	}
	if _, _, err := probeProcess(verified.Process); err != nil {
		return substrate.NodeHandle{}, err
	}
	return manifestHandle(verified), nil
}
