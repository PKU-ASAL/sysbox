package worker

import (
	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
)

type Executor struct {
	bridge *api.ExecutionBridge
}

func NewExecutor(cfg config.ServiceConfig) *Executor {
	return &Executor{bridge: api.NewExecutionBridge(cfg)}
}

func (e *Executor) Execute(run *api.Run) {
	e.bridge.Execute(run)
}
