package monitor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	providerexec "github.com/oslab/sysbox/pkg/provider/exec"
	"github.com/oslab/sysbox/pkg/sensor"
	"github.com/oslab/sysbox/pkg/vsockrpc"
)

// VmVsockBackend implements Backend for Firecracker microVMs by speaking
// vsock-rpc to sysbox-init's in-guest agent.
//
// Reachability is verified at Start() time by issuing a PING over vsock. A
// full eBPF event pipeline (analogous to the docker/tracee backend) requires
// an in-guest sensor binary which is not yet bundled with the default
// rootfs. Until then, the backend confirms wiring and holds the monitor
// session open so a future agent can be slotted in without a host-side
// rewrite.
type VmVsockBackend struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

func init() {
	Register(&VmVsockBackend{})
}

func (v *VmVsockBackend) Name() string { return "vm-vsock" }

// Start dials each VM's vsock-agent and spawns a per-VM watcher goroutine.
// Returns the event channel (currently always idle for vsock targets).
func (v *VmVsockBackend) Start(ctx context.Context, targets []Target, _ Config) (<-chan sensor.Event, error) {
	var vmTargets []vmTarget
	for _, tgt := range targets {
		if tgt.Substrate == "docker" {
			fmt.Fprintf(os.Stderr, "[monitor/vm-vsock] skip docker target %s (use tracee backend)\n", tgt.NodeID)
			continue
		}
		uds := tgt.Handle["vsock_uds"]
		if uds == "" {
			fmt.Fprintf(os.Stderr,
				"[monitor/vm-vsock] skip %s: handle has no vsock_uds (apply may pre-date sysbox-init integration)\n",
				tgt.NodeID)
			continue
		}
		var port uint32
		if s := tgt.Handle["vsock_port"]; s != "" {
			if n, err := strconv.ParseUint(s, 10, 32); err == nil {
				port = uint32(n)
			}
		}
		vmTargets = append(vmTargets, vmTarget{
			nodeID: tgt.NodeID,
			conn:   providerexec.NewVsockConnection(uds, port),
		})
	}

	if len(vmTargets) == 0 {
		return nil, fmt.Errorf("vm-vsock: no firecracker targets with vsock metadata in state")
	}

	tctx, cancel := context.WithCancel(ctx)
	v.mu.Lock()
	v.cancel = cancel
	v.mu.Unlock()

	ch := make(chan sensor.Event, 1024)

	for _, vt := range vmTargets {
		go v.watch(tctx, vt, ch)
	}

	return ch, nil
}

// Stop cancels all watcher goroutines.
func (v *VmVsockBackend) Stop(_ context.Context) error {
	v.mu.Lock()
	if v.cancel != nil {
		v.cancel()
	}
	v.mu.Unlock()
	return nil
}

type vmTarget struct {
	nodeID string
	conn   *providerexec.VsockConnection
}

// watch probes the in-guest vsock-agent, then launches the vm-sensor
// inside the VM over OpExec. Each stdout frame is line-split and parsed
// as tracee-flavoured JSON; events are pushed to ch until the parent
// context is cancelled or the sensor exits.
func (v *VmVsockBackend) watch(ctx context.Context, vt vmTarget, ch chan<- sensor.Event) {
	waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
	err := vt.conn.WaitReady(waitCtx, 30*time.Second)
	waitCancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[monitor/vm-vsock] %s: vsock-agent unreachable: %v\n", vt.nodeID, err)
		return
	}
	fmt.Printf("[monitor/vm-vsock] %s: vsock-agent reachable; launching vm-sensor\n", vt.nodeID)

	sensorCmd := []string{"/sysbox-init", "--vm-sensor",
		"--events", "execve,fork,exit",
		"--node", vt.nodeID,
	}

	var partial []byte
	handler := func(f vsockrpc.Frame) error {
		if len(f.Stdout) > 0 {
			data := f.Stdout
			if len(partial) > 0 {
				data = append(partial, data...)
				partial = nil
			}
			for {
				i := bytes.IndexByte(data, '\n')
				if i < 0 {
					partial = append(partial, data...)
					break
				}
				line := data[:i]
				data = data[i+1:]
				if len(line) == 0 {
					continue
				}
				var e sensor.Event
				if err := sensor.ParseTraceeJSON(line, &e); err == nil {
					e.NodeID = vt.nodeID // override with canonical node ID
					select {
					case ch <- e:
					default:
					}
				}
			}
		}
		return nil
	}

	err = vt.conn.ExecStream(ctx, sensorCmd, nil, handler)
	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "[monitor/vm-vsock] %s: vm-sensor ended: %v\n", vt.nodeID, err)
	}
}
