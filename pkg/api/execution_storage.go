package api

import (
	"context"
	"fmt"
	"os"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/revisiongate"
)

func openExecutionStorage(cfg config.ServiceConfig, runsDir, stateBackend string) (apiStore, *revisiongate.RemoteRegistry, error) {
	store, initErr := newAPIStore(runsDir, stateBackend)
	if initErr == nil && cfg.RevisionRegistry != "" {
		if stateBackend != "" {
			initErr = fmt.Errorf("revision_registry requires local metadata backend")
		} else {
			initErr = revisiongate.Enroll(context.Background(), runsDir, cfg.RevisionRegistry)
		}
	}
	if initErr == nil && cfg.RevisionRemote != nil {
		if stateBackend != "" || cfg.RevisionRegistry != "" || len(cfg.RevisionConsumers) > 0 {
			initErr = fmt.Errorf("remote registry consumer requires exclusive local metadata backend")
		} else {
			initErr = revisiongate.BindRemote(context.Background(), runsDir, revisiongate.RemoteBinding{URL: cfg.RevisionRemote.URL, Consumer: cfg.RevisionRemote.Consumer, TokenFile: cfg.RevisionRemote.TokenFile})
		}
	}
	var remoteRegistry *revisiongate.RemoteRegistry
	if initErr == nil && len(cfg.RevisionConsumers) > 0 {
		if stateBackend != "" {
			initErr = fmt.Errorf("remote registry service requires local registry backend")
		} else {
			var catalog string
			catalog, initErr = revisiongate.Catalog(runsDir)
			if initErr == nil {
				remoteRegistry, initErr = revisiongate.NewRemoteRegistry(catalog, cfg.RevisionConsumers)
			}
		}
	}
	if initErr == nil && (len(cfg.RevisionSQLConsumers) > 0 || cfg.RevisionRegistryID != "") {
		if stateBackend != "" {
			initErr = fmt.Errorf("federated coordinator requires file registry")
		} else {
			catalog, err := revisiongate.Catalog(runsDir)
			if err == nil {
				err = os.MkdirAll(catalog, 0700)
			}
			if err == nil {
				var release func()
				release, err = revisiongate.Lock(context.Background(), catalog)
				if err == nil {
					_, err = enrollSQLConsumers(cfg, catalog)
					release()
				}
			}
			initErr = err
		}
	}
	return store, remoteRegistry, initErr
}
