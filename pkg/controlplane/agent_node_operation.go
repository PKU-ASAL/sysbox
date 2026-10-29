package controlplane

import (
	"time"
)

func NodeOperationEvent(op NodeOperation, action, status, message string) Event {
	return Event{
		ProjectID: op.ProjectID,
		Workspace: op.Workspace,
		Resource:  op.Resource(),
		Action:    action,
		Status:    status,
		Actor:     op.RequestedBy,
		Roles:     append([]string{}, op.Roles...),
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
}
