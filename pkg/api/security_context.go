package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type requestSubject struct {
	User  string
	Roles []string
}

func (s *Server) requestSubject(r *http.Request) requestSubject {
	userHeader := s.cfg.API.Headers.User
	if userHeader == "" {
		userHeader = "X-Sysbox-User"
	}
	rolesHeader := s.cfg.API.Headers.Roles
	if rolesHeader == "" {
		rolesHeader = "X-Sysbox-Roles"
	}
	user := strings.TrimSpace(r.Header.Get(userHeader))
	if user == "" {
		user = "api"
	}
	return requestSubject{
		User:  user,
		Roles: parseRoleHeader(r.Header.Get(rolesHeader)),
	}
}

func parseRoleHeader(raw string) []string {
	seen := map[string]struct{}{}
	var roles []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		role := strings.TrimSpace(part)
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	return roles
}

func (s *Server) authorizeConsole(subj requestSubject, sess controlplane.ConsoleSession) error {
	if subj.User == "" {
		return fmt.Errorf("console session requires an authenticated subject")
	}
	allowed := append([]string{}, s.cfg.API.Console.AllowedRoles...)
	if len(allowed) == 0 {
		return nil
	}
	allowed = append(allowed, s.cfg.API.RBAC.AdminRoles...)
	for _, role := range subj.Roles {
		if slices.Contains(allowed, role) {
			return nil
		}
	}
	return fmt.Errorf("console session denied for %s: requires one of roles %s", subj.User, strings.Join(allowed, ","))
}

func auditEvent(project, workspace, resource, action, status, actor, message string, roles []string) controlplane.Event {
	return controlplane.Event{
		ProjectID: project,
		Workspace: workspace,
		Resource:  resource,
		Action:    action,
		Status:    status,
		Actor:     actor,
		Roles:     append([]string{}, roles...),
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
}
