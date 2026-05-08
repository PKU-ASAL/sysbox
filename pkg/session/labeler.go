package session

import (
	"sync"

	"github.com/oslab/sysbox/pkg/sensor"
)

// Labeler annotates sensor events with session information.
//
// Method A (strict): SessionID is populated iff the event's CgroupID appears
// in the registration table. No heuristics, no process-tree fallback.
// is_attack == (SessionID != "").
type Labeler struct {
	mu          sync.RWMutex
	cgroupTable map[uint64]string // cgroup_id → session_id
}

// NewLabeler returns an empty Labeler.
func NewLabeler() *Labeler {
	return &Labeler{cgroupTable: make(map[uint64]string)}
}

// RegisterSession adds a cgroup_id → session_id mapping.
// Called when the sshd-hook notifies us that a new session cgroup was created.
func (l *Labeler) RegisterSession(cgroupID uint64, sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cgroupTable[cgroupID] = sessionID
}

// UnregisterSession removes the mapping when a session ends.
func (l *Labeler) UnregisterSession(cgroupID uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.cgroupTable, cgroupID)
}

// Annotate fills in Event.SessionID and Event.IsAttack.
// Satisfies the sensor.Labeler interface.
func (l *Labeler) Annotate(e *sensor.Event) {
	l.mu.RLock()
	sid := l.cgroupTable[e.CgroupID]
	l.mu.RUnlock()
	e.SessionID = sid
	e.IsAttack = sid != ""
}

// Sessions returns a snapshot of all currently registered sessions.
func (l *Labeler) Sessions() map[uint64]string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[uint64]string, len(l.cgroupTable))
	for k, v := range l.cgroupTable {
		out[k] = v
	}
	return out
}
