package network

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
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
	marker := networkOwnership{Version: 2, Identity: id, Spec: spec}
	if err := d.writeOwnership(spec.Name, marker); err != nil {
		return err
	}
	createdNS, err := createExclusiveNetns(spec.Name)
	if err != nil {
		return err
	}
	defer createdNS.Close()
	_, root := d.roots()
	info, err := os.Stat(filepath.Join(root, spec.Name))
	if err != nil {
		return err
	}
	var createdStat syscall.Stat_t
	if err := syscall.Fstat(int(createdNS), &createdStat); err != nil {
		return err
	}
	actual := info.Sys().(*syscall.Stat_t)
	if actual.Dev != createdStat.Dev || actual.Ino != createdStat.Ino {
		return fmt.Errorf("created namespace was replaced")
	}
	marker.setNamespace(info)
	if err := d.writeOwnership(spec.Name, marker); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.createOwnedNetworkLinks(ctx, &marker); err != nil {
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
		return d.removeOwnership(spec.Name)
	}
	raw, err := os.ReadFile(d.ownershipPath(spec.Name))
	if err != nil {
		return err
	}
	var marker networkOwnership
	if err := json.Unmarshal(raw, &marker); err != nil {
		return err
	}
	if err := d.deleteRecordedLinks(ctx, marker); err != nil {
		return err
	}
	observed, err = d.ObserveIsolated(ctx, spec)
	if err != nil {
		return err
	}
	if observed.Exists {
		return fmt.Errorf("isolated network deletion left managed objects")
	}
	return d.removeOwnership(spec.Name)
}

// Called under the host lock after absence and ownership have been verified.
func (d Driver) removeOwnership(name string) error {
	if err := os.Remove(d.ownershipPath(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	root, _ := d.roots()
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
