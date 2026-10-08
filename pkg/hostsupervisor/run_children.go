package hostsupervisor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
)

// RunChildren owns the durable guest tasks of one host Run. Configure its
// exported fields before starting tasks; a completed owner cannot be reused.
type RunChildren struct {
	Dispatcher *Dispatcher
	RenewGrant func(context.Context, controlplane.SignedExecutionGrant, controlplane.SignedExecutionGrant) (controlplane.SignedExecutionGrant, error)

	mu         sync.Mutex
	wg         sync.WaitGroup
	children   map[string]struct{}
	stopCtx    context.Context
	stop       context.CancelFunc
	closed     bool
	failures   []error
	finishOnce sync.Once
	finished   chan struct{}
}

func (r *RunChildren) initializeLocked() {
	if r.children == nil {
		r.children = make(map[string]struct{})
		r.stopCtx, r.stop = context.WithCancel(context.Background())
		r.finished = make(chan struct{})
	}
}

// Start persists intent through Dispatcher before any provider effect. Even a
// lost start reply leaves a child to reconcile, with the same durable handle.
func (r *RunChildren) Start(ctx context.Context, ref TargetRef, req guesttask.Request) (substrate.ExecutionHandle, error) {
	r.mu.Lock()
	r.initializeLocked()
	if r.closed || r.stopCtx.Err() != nil {
		r.mu.Unlock()
		return substrate.ExecutionHandle{}, fmt.Errorf("run children are closed")
	}
	if r.Dispatcher == nil {
		r.mu.Unlock()
		return substrate.ExecutionHandle{}, fmt.Errorf("run child dispatcher unavailable")
	}
	// Reserve before releasing admission. Wait can seal admission at any point,
	// but can never observe a zero count while an admitted Start is in flight.
	r.wg.Add(1)
	r.mu.Unlock()
	startCtx, cancel := context.WithCancel(ctx)
	unregister := context.AfterFunc(r.stopCtx, cancel)
	unregisterParent := context.AfterFunc(ctx, func() { r.fail(ctx.Err()) })
	record, err := r.Dispatcher.Start(startCtx, ref, req)
	unregisterParent()
	unregister()
	cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	watching := false
	if record.Handle.ID != "" {
		key := recordKey(record.Handle)
		if _, exists := r.children[key]; !exists {
			r.children[key] = struct{}{}
			watching = true
			go r.watch(ctx, record)
		}
	}
	if !watching {
		r.wg.Done()
	}
	if err != nil {
		r.failures = append(r.failures, err)
		r.stop()
	}
	return record.Handle, err
}

func (r *RunChildren) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.failures = append(r.failures, err)
	}
	r.stop()
}

func (r *RunChildren) watch(parent context.Context, record TaskRecord) {
	defer r.wg.Done()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	unregister := context.AfterFunc(r.stopCtx, cancel)
	defer unregister()
	unregisterParent := context.AfterFunc(parent, func() { r.fail(parent.Err()) })
	defer unregisterParent()
	if err := r.observe(ctx, record); err != nil {
		r.fail(err)
		// The parent's canceled deadline must not prevent owned cleanup.
		cleanup, done := context.WithTimeout(context.WithoutCancel(parent), recoveryTaskBudget)
		defer done()
		if err := r.cleanup(cleanup, record); err != nil {
			r.fail(fmt.Errorf("child %s cleanup: %w", record.Handle.ID, err))
		}
	}
}

func (r *RunChildren) observe(ctx context.Context, record TaskRecord) error {
	budget, err := controlplane.ExecutionGrantBudget(record.Grant, time.Now())
	if err != nil {
		return err
	}
	renew := time.NewTimer(budget / 3)
	defer renew.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		call, cancel := context.WithTimeout(ctx, recoveryTaskBudget)
		current, err := r.Dispatcher.Observe(call, record.Ref, record.Handle)
		cancel()
		if err != nil {
			return err
		}
		if current.StopReceipt != nil && current.Reported {
			if !current.Result.ExitCodeKnown {
				return fmt.Errorf("child %s stopped with unknown exit status", record.Handle.ID)
			}
			if current.Result.ExitCode != 0 {
				return fmt.Errorf("child %s exited with status %d", record.Handle.ID, current.Result.ExitCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
		case <-renew.C:
			parent, ok := transport.ParentExecutionGrantFromContext(ctx)
			if !ok || r.RenewGrant == nil {
				return fmt.Errorf("run child renewal authority unavailable")
			}
			call, cancel := context.WithTimeout(ctx, recoveryTaskBudget)
			grant, err := r.RenewGrant(call, parent, current.Grant)
			if err == nil {
				record, err = r.Dispatcher.Renew(call, record.Ref, record.Handle, grant)
			}
			cancel()
			if err != nil {
				return err
			}
			budget, err = controlplane.ExecutionGrantBudget(grant, time.Now())
			if err != nil {
				return err
			}
			renew.Reset(budget / 3)
		}
	}
}

func (r *RunChildren) cleanup(ctx context.Context, record TaskRecord) error {
	current, err := r.Dispatcher.Cancel(ctx, record.Ref, record.Handle)
	if err == nil && current.StopReceipt != nil && current.Reported {
		return nil
	}
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-poll.C:
			current, err = r.Dispatcher.Observe(ctx, record.Ref, record.Handle)
			if err == nil && current.StopReceipt != nil && current.Reported {
				return nil
			}
		}
	}
}

// Wait seals admission and waits for natural completion. Services remain
// active until the parent is canceled; they are never silently stopped here.
func (r *RunChildren) Wait(ctx context.Context) error {
	r.mu.Lock()
	r.initializeLocked()
	r.closed = true
	r.mu.Unlock()
	r.finishOnce.Do(func() {
		go func() { r.wg.Wait(); close(r.finished) }()
	})
	select {
	case <-r.finished:
	case <-ctx.Done():
		r.fail(ctx.Err())
	case <-r.stopCtx.Done():
	}
	// Provider implementations may ignore context cancellation. Give owned
	// cleanup a bounded opportunity, then report uncertainty without inventing
	// stop evidence or dropping the durable task record.
	timer := time.NewTimer(recoveryTaskBudget)
	defer timer.Stop()
	var unfinished error
	select {
	case <-r.finished:
	case <-timer.C:
		unfinished = fmt.Errorf("run child termination remains unconfirmed: %w", context.DeadlineExceeded)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(append(append([]error{}, r.failures...), ctx.Err(), unfinished)...)
}

// Cancel stops all owned children and waits for bounded reconciliation. Any
// unconfirmed stop remains in the Dispatcher journal for recovery.
func (r *RunChildren) Cancel(ctx context.Context) error {
	r.mu.Lock()
	r.initializeLocked()
	r.closed = true
	r.stop()
	r.mu.Unlock()
	return r.Wait(ctx)
}

// Finish timestamps the entire Run only after every owned child has completed.
func (r *RunChildren) Finish(ctx context.Context, run *controlplane.Run) error {
	if run == nil || run.Status != controlplane.RunDone {
		return fmt.Errorf("successful parent outcome required before waiting for children")
	}
	if err := r.Wait(ctx); err != nil {
		return err
	}
	run.EndedAt = time.Now().UTC()
	return nil
}
