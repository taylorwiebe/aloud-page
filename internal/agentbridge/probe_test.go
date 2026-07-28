package agentbridge

import "testing"

func TestProbeCodexRequiresCurrentTaskIdentity(t *testing.T) {
	result := Probe("codex", map[string]string{
		"CODEX_THREAD_ID":          "thread-123",
		"CODEX_PERMISSION_PROFILE": "managed",
	})
	if !result.Supported {
		t.Fatalf("Probe() supported = false, reason = %q", result.Reason)
	}
	if result.TaskID != "thread-123" {
		t.Fatalf("Probe() task ID = %q", result.TaskID)
	}
	for _, capability := range []Capability{
		CapabilitySameTask,
		CapabilityWorkspace,
		CapabilityStreaming,
		CapabilityAuthorization,
		CapabilityCancellation,
		CapabilityReconnect,
	} {
		if !result.Has(capability) {
			t.Errorf("Probe() missing capability %q", capability)
		}
	}
}

func TestProbeCodexFailsClosedWithoutCurrentTaskIdentity(t *testing.T) {
	result := Probe("codex", map[string]string{})
	if result.Supported {
		t.Fatal("Probe() supported Codex without a current task identity")
	}
	if result.TaskID != "" {
		t.Fatalf("Probe() exposed unexpected task ID %q", result.TaskID)
	}
}

func TestProbeClaudeStaysUnsupportedWithoutVerifiedIdentity(t *testing.T) {
	result := Probe("claude", map[string]string{
		"CLAUDE_CODE_ENTRYPOINT": "cli",
	})
	if result.Supported {
		t.Fatal("Probe() claimed Claude support without a verified current-session identity")
	}
}

func TestProbeRejectsUnknownProviders(t *testing.T) {
	result := Probe("other", map[string]string{"CODEX_THREAD_ID": "thread-123"})
	if result.Supported {
		t.Fatal("Probe() supported an unknown provider")
	}
}
