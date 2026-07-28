package agentbridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPSeparatesBrowserAndTaskCredentialsAndBoundsRequests(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	handler := NewHTTPHandler(broker, "http://127.0.0.1:8080")
	body := `{"id":"turn-1","controller_id":"controller-1","text":"question"}`

	for name, testCase := range map[string]struct {
		mutate func(*http.Request)
		want   int
	}{
		"valid": {func(r *http.Request) {
			r.Header.Set("Origin", "http://127.0.0.1:8080")
			r.Header.Set("Content-Type", "application/json")
		}, http.StatusCreated},
		"bad origin": {func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
			r.Header.Set("Content-Type", "application/json")
		}, http.StatusForbidden},
		"bad content": {func(r *http.Request) {
			r.Header.Set("Origin", "http://127.0.0.1:8080")
			r.Header.Set("Content-Type", "text/plain")
		}, http.StatusUnsupportedMediaType},
		"wrong method": {func(r *http.Request) { r.Method = http.MethodGet; r.Header.Set("Origin", "http://127.0.0.1:8080") }, http.StatusMethodNotAllowed},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/browser/turns", strings.NewReader(body))
			testCase.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}

	oversized := httptest.NewRequest(http.MethodPost, "/browser/turns", bytes.NewReader(make([]byte, maxRequestBytes+1)))
	oversized.Header.Set("Origin", "http://127.0.0.1:8080")
	oversized.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, oversized)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d", response.Code)
	}

	taskRequest := httptest.NewRequest(http.MethodGet, "/task/turns/wait", nil)
	taskRequest.Header.Set(taskSecretHeader, "browser-token")
	taskResponse := httptest.NewRecorder()
	handler.ServeHTTP(taskResponse, taskRequest)
	if taskResponse.Code != http.StatusUnauthorized {
		t.Fatalf("browser credential reached task endpoint: %d", taskResponse.Code)
	}
}

func TestHTTPReturnsAgentHTMLAsUntrustedJSONText(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "question"})
	untrusted := `<img src=x onerror=alert(1)><script>alert("x")</script>`
	_, err := broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventText, Text: untrusted, Diff: untrusted})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPHandler(broker, "http://127.0.0.1:8080")
	request := httptest.NewRequest(http.MethodGet, "/browser/events?after=0", nil)
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if strings.Contains(response.Body.String(), `\u003c`) == false {
		t.Fatalf("HTML was not JSON-escaped: %s", response.Body.String())
	}
	var events []Event
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if events[0].Text != untrusted || events[0].Diff != untrusted {
		t.Fatalf("untrusted text changed = %#v", events[0])
	}
}

func TestHTTPTaskCanWaitForBrowserDecision(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "change it"})
	proposal := Proposal{ID: "action-1", TurnID: turn.ID, Digest: "digest-1", DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute)}
	_, _ = broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal})
	_, _ = broker.Decide(Decision{ID: "decision-1", ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision, Approved: true})

	handler := NewHTTPHandler(broker, "http://127.0.0.1:8080")
	request := httptest.NewRequest(http.MethodGet, "/task/decisions?action_id=action-1", nil)
	request.Header.Set(taskSecretHeader, "task-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}
