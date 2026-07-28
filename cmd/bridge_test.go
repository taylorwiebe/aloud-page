package cmd

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/taylorwiebe/planreader/internal/agentbridge"
)

func TestBridgeProbeReportsCurrentCodexTask(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "thread-123")
	t.Setenv("CODEX_PERMISSION_PROFILE", "managed")

	var stdout bytes.Buffer
	command := newBridgeCommand(&stdout)
	command.SetArgs([]string{"probe", "--provider", "codex"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	var result agentbridge.ProbeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Supported || result.TaskID != "thread-123" {
		t.Fatalf("probe result = %#v", result)
	}
}

func TestBridgeProbeFailsClosedForUnverifiedClaudeTask(t *testing.T) {
	var stdout bytes.Buffer
	command := newBridgeCommand(&stdout)
	command.SetArgs([]string{"probe", "--provider", "claude"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}

	var result agentbridge.ProbeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Supported {
		t.Fatalf("probe result = %#v", result)
	}
}
