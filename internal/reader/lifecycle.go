package reader

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
)

const agentSessionHeader = "X-Planreader-Session"

type agentLifecycle struct {
	mu           sync.Mutex
	sessions     map[string]time.Time
	idleTimeout  time.Duration
	shutdown     func()
	shutdownOnce sync.Once
	done         chan struct{}
	closeOnce    sync.Once
	reloadGrace  time.Duration
	startedAt    time.Time
	warn         func()
	browserLease interface {
		BrowserHeartbeat(string) agentbridge.BrowserRole
		BrowserRelease(string)
	}
}

func newAgentLifecycle(shutdown func(), idleTimeout, maximumLifetime time.Duration) *agentLifecycle {
	return newAgentLifecycleWithWarning(shutdown, nil, idleTimeout, maximumLifetime, 5*time.Second)
}

func newAgentLifecycleWithWarning(shutdown, warn func(), idleTimeout, maximumLifetime, reloadGrace time.Duration) *agentLifecycle {
	lifecycle := &agentLifecycle{
		sessions:    make(map[string]time.Time),
		idleTimeout: idleTimeout,
		shutdown:    shutdown,
		done:        make(chan struct{}),
		reloadGrace: reloadGrace,
		startedAt:   time.Now(),
		warn:        warn,
	}
	go lifecycle.watch(maximumLifetime)
	return lifecycle
}

func (l *agentLifecycle) Heartbeat(sessionID string) {
	if sessionID = strings.TrimSpace(sessionID); sessionID == "" {
		return
	}
	l.mu.Lock()
	l.sessions[sessionID] = time.Now()
	l.mu.Unlock()
	if l.browserLease != nil {
		l.browserLease.BrowserHeartbeat(sessionID)
	}
}

func (l *agentLifecycle) Release(sessionID string) {
	l.mu.Lock()
	sessionID = strings.TrimSpace(sessionID)
	if _, ok := l.sessions[sessionID]; ok {
		l.sessions[sessionID] = time.Now().Add(l.reloadGrace - l.idleTimeout)
	}
	l.mu.Unlock()
	if l.browserLease != nil {
		l.browserLease.BrowserRelease(sessionID)
	}
}

func (l *agentLifecycle) Close() {
	l.closeOnce.Do(func() { close(l.done) })
}

func (l *agentLifecycle) requestShutdown() {
	l.shutdownOnce.Do(func() { go l.shutdown() })
}

func (l *agentLifecycle) watch(maximumLifetime time.Duration) {
	checkEvery := l.idleTimeout / 4
	if checkEvery < 5*time.Millisecond {
		checkEvery = 5 * time.Millisecond
	}
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	maximum := time.NewTimer(maximumLifetime)
	defer maximum.Stop()
	var warning <-chan time.Time
	if l.warn != nil {
		warningAt := maximumLifetime - l.reloadGrace
		if warningAt < 0 {
			warningAt = 0
		}
		warningTimer := time.NewTimer(warningAt)
		defer warningTimer.Stop()
		warning = warningTimer.C
	}
	for {
		select {
		case now := <-ticker.C:
			l.mu.Lock()
			for id, lastSeen := range l.sessions {
				if now.Sub(lastSeen) >= l.idleTimeout {
					delete(l.sessions, id)
				}
			}
			idle := len(l.sessions) == 0 && now.Sub(l.startedAt) >= l.idleTimeout
			l.mu.Unlock()
			if idle {
				l.requestShutdown()
				return
			}
		case <-maximum.C:
			l.requestShutdown()
			return
		case <-warning:
			l.warn()
			warning = nil
		case <-l.done:
			return
		}
	}
}

func (l *agentLifecycle) ServeHTTP(w http.ResponseWriter, r *http.Request, endpoint string) {
	switch endpoint {
	case "agent/heartbeat":
		switch r.Method {
		case http.MethodPost:
			l.Heartbeat(r.Header.Get(agentSessionHeader))
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			l.Release(r.Header.Get(agentSessionHeader))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "agent/shutdown":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		l.requestShutdown()
	default:
		http.NotFound(w, r)
	}
}
