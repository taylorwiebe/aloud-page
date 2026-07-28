package agentbridge

import (
	"sync"
	"time"
)

type BrowserRole string

const (
	BrowserController BrowserRole = "controller"
	BrowserObserver   BrowserRole = "observer"
)

type LeaseSnapshot struct {
	ControllerID  string
	BrowserCount  int
	TaskConnected bool
	Closed        bool
}

// LeaseManager owns connection lifetimes only. Presentation state is
// deliberately absent from its state so hiding the dock cannot release work.
type LeaseManager struct {
	mu            sync.Mutex
	attachmentID  string
	reloadGrace   time.Duration
	taskTTL       time.Duration
	browsers      map[string]time.Time
	controllerID  string
	taskExpiresAt time.Time
	taskLeaseSeen bool
	closed        bool
}

func NewLeaseManager(attachmentID string, reloadGrace, taskTTL time.Duration) *LeaseManager {
	return &LeaseManager{
		attachmentID: attachmentID,
		reloadGrace:  reloadGrace,
		taskTTL:      taskTTL,
		browsers:     make(map[string]time.Time),
	}
}

func (l *LeaseManager) BrowserHeartbeat(id string) BrowserRole {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireBrowsersLocked(time.Now())
	if id == "" || l.closed {
		return BrowserObserver
	}
	l.browsers[id] = time.Time{}
	if l.controllerID == "" {
		l.controllerID = id
	}
	if l.controllerID == id {
		return BrowserController
	}
	return BrowserObserver
}

func (l *LeaseManager) BrowserRelease(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.browsers[id]; ok {
		l.browsers[id] = time.Now().Add(l.reloadGrace)
	}
}

func (l *LeaseManager) TaskHeartbeat(attachmentID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if attachmentID != l.attachmentID {
		return ErrAttachment
	}
	if l.closed {
		return ErrClosed
	}
	l.taskLeaseSeen = true
	l.taskExpiresAt = time.Now().Add(l.taskTTL)
	return nil
}

func (l *LeaseManager) TaskConnected() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return !l.closed && l.taskLeaseSeen && time.Now().Before(l.taskExpiresAt)
}

func (l *LeaseManager) Snapshot() LeaseSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireBrowsersLocked(time.Now())
	return LeaseSnapshot{
		ControllerID:  l.controllerID,
		BrowserCount:  len(l.browsers),
		TaskConnected: !l.closed && l.taskLeaseSeen && time.Now().Before(l.taskExpiresAt),
		Closed:        l.closed,
	}
}

// SetDockState is intentionally a no-op: dock visibility is presentation,
// never ownership or cancellation.
func (l *LeaseManager) SetDockState(string) {}

func (l *LeaseManager) Close() {
	l.mu.Lock()
	l.closed = true
	l.browsers = make(map[string]time.Time)
	l.controllerID = ""
	l.taskExpiresAt = time.Time{}
	l.mu.Unlock()
}

func (l *LeaseManager) expireBrowsersLocked(now time.Time) {
	for id, expiresAt := range l.browsers {
		if !expiresAt.IsZero() && !now.Before(expiresAt) {
			delete(l.browsers, id)
			if l.controllerID == id {
				l.controllerID = ""
			}
		}
	}
}
