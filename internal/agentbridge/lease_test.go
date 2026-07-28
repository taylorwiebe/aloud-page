package agentbridge

import (
	"errors"
	"testing"
	"time"
)

func TestBrowserReloadGracePreservesControllerAndObserverTakeover(t *testing.T) {
	leases := NewLeaseManager("attachment-1", 30*time.Millisecond, time.Minute)
	defer leases.Close()
	if got := leases.BrowserHeartbeat("tab-1"); got != BrowserController {
		t.Fatalf("first browser role = %q, want controller", got)
	}
	leases.BrowserRelease("tab-1")
	if got := leases.BrowserHeartbeat("tab-2"); got != BrowserObserver {
		t.Fatalf("second browser during grace = %q, want observer", got)
	}
	if got := leases.BrowserHeartbeat("tab-1"); got != BrowserController {
		t.Fatalf("reloaded browser role = %q, want controller", got)
	}
	leases.BrowserRelease("tab-1")
	time.Sleep(40 * time.Millisecond)
	if got := leases.BrowserHeartbeat("tab-2"); got != BrowserController {
		t.Fatalf("observer after controller grace = %q, want controller", got)
	}
}

func TestTaskLeaseExpiresAndSameAttachmentReconnects(t *testing.T) {
	leases := NewLeaseManager("attachment-1", time.Minute, 20*time.Millisecond)
	defer leases.Close()
	if err := leases.TaskHeartbeat("attachment-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if leases.TaskConnected() {
		t.Fatal("expired task still connected")
	}
	if err := leases.TaskHeartbeat("other"); !errors.Is(err, ErrAttachment) {
		t.Fatalf("other attachment reconnect error = %v", err)
	}
	if err := leases.TaskHeartbeat("attachment-1"); err != nil || !leases.TaskConnected() {
		t.Fatalf("same attachment reconnect = %v, connected=%v", err, leases.TaskConnected())
	}
}

func TestDockStateDoesNotChangeLeases(t *testing.T) {
	leases := NewLeaseManager("attachment-1", time.Minute, time.Minute)
	defer leases.Close()
	leases.BrowserHeartbeat("tab-1")
	if err := leases.TaskHeartbeat("attachment-1"); err != nil {
		t.Fatal(err)
	}
	before := leases.Snapshot()
	for _, state := range []string{"compact", "open", "expanded", "closed"} {
		leases.SetDockState(state)
	}
	if after := leases.Snapshot(); before != after {
		t.Fatalf("dock state changed leases: before=%+v after=%+v", before, after)
	}
}
