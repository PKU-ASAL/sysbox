package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

// A host-wide lock also protects the shortened root-link names against name
// collisions across topologies. It never grants ownership of existing links.
func (d Driver) lockNetworks(ctx context.Context) (func(), error) {
	root, _ := d.roots()
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "host.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			file.Close()
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (d Driver) tagLink(namespace, name string, id substrate.OperationIdentity) error {
	if name == "" {
		return nil
	}
	h, err := d.openNetlink(namespace)
	if err != nil {
		return err
	}
	defer h.Close()
	link, err := h.LinkByName(name)
	if err != nil {
		return err
	}
	return h.LinkSetAlias(link, networkAlias(id))
}
func (d Driver) createOwnedIsolated(ctx context.Context, spec driver.IsolatedNetworkSpec) error {
	id, err := validateNetworkRequest(ctx, spec)
	if err != nil {
		return err
	}
	unlock, err := d.lockNetworks(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	observed, err := d.ObserveIsolated(ctx, spec)
	if err != nil {
		return err
	}
	if observed.Exists {
		return driver.Wrap(driver.ErrorInvalidState, "network", "network already exists; creation was not authorized", nil)
	}
	marker := networkOwnership{Version: 1, Identity: id, Spec: spec}
	if err := d.writeOwnership(spec.Name, marker); err != nil {
		return err
	}
	if err := CreateNetns(spec.Name); err != nil {
		return err
	}
	_, root := d.roots()
	info, err := os.Stat(filepath.Join(root, spec.Name))
	if err != nil {
		return err
	}
	marker.setNamespace(info)
	if err := d.writeOwnership(spec.Name, marker); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CreateBridge(BridgeConfig{NetnsName: spec.Name, BridgeName: spec.Bridge, CIDR: spec.CIDR}); err != nil {
		return err
	}
	if err := d.tagLink(spec.Name, spec.Bridge, id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := CreateRootBridgeProxy(spec); err != nil {
		return err
	}
	for _, name := range []string{spec.RootBridge, spec.RootEnd} {
		if err := d.tagLink("", name, id); err != nil {
			return err
		}
	}
	if err := d.tagLink(spec.Name, spec.NamespaceEnd, id); err != nil {
		return err
	}
	observed, err = d.ObserveIsolated(ctx, spec)
	if err != nil {
		return err
	}
	if !observed.Healthy {
		return fmt.Errorf("isolated network verification failed: %s", observed.Reason)
	}
	return nil
}
func (d Driver) deleteOwnedIsolated(ctx context.Context, spec driver.IsolatedNetworkSpec) error {
	if _, err := validateNetworkRequest(ctx, spec); err != nil {
		return err
	}
	unlock, err := d.lockNetworks(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	observed, err := d.ObserveIsolated(ctx, spec)
	if err != nil {
		return err
	}
	if !observed.Exists {
		return nil
	}
	if err := DeleteRootBridgeProxy(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, root := d.roots()
	_, err = os.Stat(filepath.Join(root, spec.Name))
	if err == nil {
		if err := DeleteBridge(BridgeConfig{NetnsName: spec.Name, BridgeName: spec.Bridge}); err != nil {
			return err
		}
		if err := DeleteNetns(spec.Name); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	observed, err = d.ObserveIsolated(ctx, spec)
	if err != nil {
		return err
	}
	if observed.Exists {
		return fmt.Errorf("isolated network deletion left managed objects")
	}
	if err := os.Remove(d.ownershipPath(spec.Name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
