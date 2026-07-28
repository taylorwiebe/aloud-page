package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestBridgeWaitUsesOwnerOnlyDescriptor(t *testing.T) {
	var gotSecret, gotAttachment string
	client := testHTTPClient(func(r *http.Request) *http.Response {
		gotSecret = r.Header.Get("X-Planreader-Task-Secret")
		gotAttachment = r.URL.Query().Get("attachment_id")
		return bridgeResponse(http.StatusOK, `{"id":"turn-1"}`)
	})

	var stdout bytes.Buffer
	command := newBridgeCommandWithClient(&stdout, client)
	command.SetArgs([]string{"wait", "--descriptor", writeTestDescriptor(t)})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotSecret != "task-secret" || gotAttachment != "attachment-1" {
		t.Fatalf("secret = %q, attachment = %q", gotSecret, gotAttachment)
	}
}

func TestBridgePublishForwardsNormalizedEventAsJSON(t *testing.T) {
	var gotType, gotSecret string
	client := testHTTPClient(func(r *http.Request) *http.Response {
		gotSecret = r.Header.Get("X-Planreader-Task-Secret")
		var event agentbridge.Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Fatal(err)
		}
		gotType = string(event.Type)
		return bridgeResponse(http.StatusCreated, "")
	})

	command := newBridgeCommandWithClient(io.Discard, client)
	command.SetIn(strings.NewReader(`{"id":"event-1","turn_id":"turn-1","sequence":1,"type":"progress"}`))
	command.SetArgs([]string{"publish", "--descriptor", writeTestDescriptor(t)})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotSecret != "task-secret" || gotType != "progress" {
		t.Fatalf("secret = %q, type = %q", gotSecret, gotType)
	}
}

func TestBridgeDecisionWaitsForOneProposalDecision(t *testing.T) {
	var gotActionID string
	client := testHTTPClient(func(r *http.Request) *http.Response {
		gotActionID = r.URL.Query().Get("action_id")
		return bridgeResponse(http.StatusOK, `{"id":"decision-1","action_id":"action-1","approved":true}`)
	})

	var stdout bytes.Buffer
	command := newBridgeCommandWithClient(&stdout, client)
	command.SetArgs([]string{"decision", "--descriptor", writeTestDescriptor(t), "--action", "action-1"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotActionID != "action-1" || !strings.Contains(stdout.String(), `"approved":true`) {
		t.Fatalf("action ID = %q, output = %s", gotActionID, stdout.String())
	}
}

func TestBridgeRejectsDescriptorReadableByOtherUsers(t *testing.T) {
	path := writeTestDescriptor(t)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	command := newBridgeCommandWithClient(io.Discard, testHTTPClient(func(*http.Request) *http.Response {
		t.Fatal("insecure descriptor must not be sent")
		return nil
	}))
	command.SetArgs([]string{"wait", "--descriptor", path})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("wait error = %v", err)
	}
}

func TestBridgeRejectsDescriptorProtocolMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "descriptor.json")
	if err := os.WriteFile(path, []byte(`{"version":"future","attachment_id":"attachment-1","endpoint":"http://127.0.0.1/bridge","task_secret":"task-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := newBridgeCommandWithClient(io.Discard, testHTTPClient(func(*http.Request) *http.Response {
		t.Fatal("mismatched descriptor must not be sent")
		return nil
	}))
	command.SetArgs([]string{"wait", "--descriptor", path})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("wait error = %v", err)
	}
}

func writeTestDescriptor(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "descriptor.json")
	data := `{"version":"v1","attachment_id":"attachment-1","endpoint":"http://127.0.0.1/bridge","task_secret":"task-secret"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request), nil
}

func testHTTPClient(roundTrip roundTripFunc) *http.Client {
	return &http.Client{Transport: roundTrip}
}

func bridgeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
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
