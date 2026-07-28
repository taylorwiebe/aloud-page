package reader

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentLifecycleReleaseUsesReloadGrace(t *testing.T) {
	shutdown := make(chan struct{}, 1)
	lifecycle := newAgentLifecycleWithWarning(func() { shutdown <- struct{}{} }, nil, 20*time.Millisecond, time.Hour, 10*time.Millisecond)
	defer lifecycle.Close()
	lifecycle.Heartbeat("tab")
	lifecycle.Release("tab")
	time.Sleep(5 * time.Millisecond)
	lifecycle.Heartbeat("tab")
	select {
	case <-shutdown:
		t.Fatal("reload within grace shut down reader")
	case <-time.After(12 * time.Millisecond):
	}
	lifecycle.Release("tab")
	select {
	case <-shutdown:
	case <-time.After(60 * time.Millisecond):
		t.Fatal("released browser did not shut down after grace")
	}
}

func TestAgentLifecycleWarnsBeforeMaximumLifetime(t *testing.T) {
	var warned atomic.Bool
	shutdown := make(chan struct{}, 1)
	lifecycle := newAgentLifecycleWithWarning(func() { shutdown <- struct{}{} }, func() { warned.Store(true) }, time.Hour, 35*time.Millisecond, 15*time.Millisecond)
	defer lifecycle.Close()
	lifecycle.Heartbeat("tab")
	select {
	case <-shutdown:
		t.Fatal("shutdown happened before warning")
	case <-time.After(25 * time.Millisecond):
		if !warned.Load() {
			t.Fatal("maximum lifetime warning was not emitted")
		}
	}
	select {
	case <-shutdown:
	case <-time.After(40 * time.Millisecond):
		t.Fatal("maximum lifetime did not terminate reader")
	}
}
