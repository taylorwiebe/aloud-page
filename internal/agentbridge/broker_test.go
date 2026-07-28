package agentbridge

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestBrokerDeduplicatesIDsAndRejectsEventGaps(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "question"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := broker.SubmitTurn(turn)
	if err != nil || duplicate.ID != turn.ID {
		t.Fatalf("duplicate turn = %#v, %v", duplicate, err)
	}
	if _, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "changed"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed duplicate error = %v", err)
	}
	if _, err := broker.Publish(Event{ID: "event-2", TurnID: turn.ID, Sequence: 2, Type: EventProgress, Text: "gap"}); !errors.Is(err, ErrSequence) {
		t.Fatalf("sequence gap error = %v", err)
	}
	event, err := broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProgress, Text: "working"})
	if err != nil {
		t.Fatal(err)
	}
	duplicateEvent, err := broker.Publish(event)
	if err != nil || duplicateEvent.Sequence != event.Sequence {
		t.Fatalf("duplicate event = %#v, %v", duplicateEvent, err)
	}
}

func TestBrokerAllowsOnlyOneControllerAndActiveTurn(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	if _, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.SubmitTurn(Turn{ID: "turn-2", ControllerID: "controller-1", Text: "second"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second active turn error = %v", err)
	}
	if _, err := broker.SubmitTurn(Turn{ID: "turn-3", ControllerID: "controller-2", Text: "other tab"}); !errors.Is(err, ErrNotController) {
		t.Fatalf("second controller error = %v", err)
	}
}

func TestBrokerShutdownWakesWaitersAndFreezesSubmissions(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	waited := make(chan error, 1)
	go func() {
		_, err := broker.WaitTurn(context.Background(), "attachment-1")
		waited <- err
	}()
	broker.Close()
	select {
	case err := <-waited:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("wait error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wake task waiter")
	}
	if _, err := broker.SubmitTurn(Turn{ID: "turn", ControllerID: "tab", Text: "question"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("submit after shutdown error = %v", err)
	}
}

func TestBrokerTaskExpiryFreezesSubmissionAndReconnects(t *testing.T) {
	broker := NewBrokerWithLeases("attachment-1", "task-secret", time.Minute, 20*time.Millisecond)
	defer broker.Close()
	if err := broker.TaskHeartbeat("attachment-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "tab", Text: "question"}); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("submit with expired task error = %v", err)
	}
	if err := broker.TaskHeartbeat("attachment-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "tab", Text: "question"}); err != nil {
		t.Fatalf("submit after reconnect: %v", err)
	}
}

func TestBrokerTaskExpiryDisconnectsAndReleasesActiveTurn(t *testing.T) {
	broker := NewBrokerWithLeases("attachment-1", "task-secret", time.Minute, 20*time.Millisecond)
	defer broker.Close()
	if err := broker.TaskHeartbeat("attachment-1"); err != nil {
		t.Fatal(err)
	}
	turn, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "tab", Text: "question"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events := broker.Replay(0)
		if len(events) == 1 && events[0].Type == EventDisconnected {
			if events[0].TurnID != turn.ID || !events[0].Interrupted {
				t.Fatalf("disconnect event = %#v", events[0])
			}
			if broker.active != nil {
				t.Fatal("expired task retained its active turn")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task expiry did not publish disconnection")
}

func TestBrokerApprovedProposalRequiresMatchingApproval(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "tab", Text: "change"})
	proposal := Proposal{ID: "action-1", TurnID: turn.ID, Digest: "digest-1", DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute)}
	_, _ = broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal})
	request := ReconcileRequest{ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision}
	if _, err := broker.ApprovedProposal(request); !errors.Is(err, ErrStaleProposal) {
		t.Fatalf("unapproved proposal error = %v", err)
	}
	_, _ = broker.Decide(Decision{ID: "decision-1", ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision, Approved: true})
	if got, err := broker.ApprovedProposal(request); err != nil || got.ID != proposal.ID {
		t.Fatalf("approved proposal = %#v, %v", got, err)
	}
}

func TestBrokerObserverTakesOverAfterControllerReleaseWithoutDuplicatingTurn(t *testing.T) {
	broker := NewBrokerWithLeases("attachment-1", "task-secret", 15*time.Millisecond, time.Minute)
	defer broker.Close()
	if err := broker.TaskHeartbeat("attachment-1"); err != nil {
		t.Fatal(err)
	}
	if role := broker.BrowserHeartbeat("tab-1"); role != BrowserController {
		t.Fatalf("tab-1 role = %q", role)
	}
	if role := broker.BrowserHeartbeat("tab-2"); role != BrowserObserver {
		t.Fatalf("tab-2 role = %q", role)
	}
	turn, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "tab-1", Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Publish(Event{ID: "done-1", TurnID: turn.ID, Sequence: 1, Type: EventCompleted}); err != nil {
		t.Fatal(err)
	}
	broker.BrowserRelease("tab-1")
	time.Sleep(20 * time.Millisecond)
	if role := broker.BrowserHeartbeat("tab-2"); role != BrowserController {
		t.Fatalf("observer takeover role = %q", role)
	}
	if _, err := broker.SubmitTurn(Turn{ID: "turn-2", ControllerID: "tab-2", Text: "second"}); err != nil {
		t.Fatalf("new controller submit: %v", err)
	}
	if len(broker.turns) != 2 {
		t.Fatalf("turn count = %d, want 2 unique turns", len(broker.turns))
	}
}

func TestBrokerReplayAndDisconnectMarksInterruptedTurn(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "question"})
	_, _ = broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProgress, Text: "working"})
	_, _ = broker.Publish(Event{ID: "event-2", TurnID: turn.ID, Sequence: 2, Type: EventText, Text: "partial"})
	broker.Disconnect()

	events := broker.Replay(1)
	if len(events) != 2 || events[0].ID != "event-2" || events[1].Type != EventDisconnected || !events[1].Interrupted {
		t.Fatalf("replay after disconnect = %#v", events)
	}
}

func TestBrokerWaitDeliversTurnOnceAcrossReconnect(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	want, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "question"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := broker.WaitTurn(ctx, "")
	if err != nil || got.ID != want.ID {
		t.Fatalf("WaitTurn() = %#v, %v", got, err)
	}
	replayed, err := broker.WaitTurn(ctx, "different-attachment")
	if !errors.Is(err, ErrAttachment) || replayed.ID != "" {
		t.Fatalf("wrong attachment wait = %#v, %v", replayed, err)
	}
}

func TestBrokerRejectsStaleProposalDecision(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "change it"})
	proposal := Proposal{ID: "action-1", TurnID: turn.ID, Digest: "digest-1", DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal}); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Decide(Decision{ID: "decision-1", ActionID: proposal.ID, ProposalDigest: "changed", DocumentRevision: "rev-1", Approved: true}); !errors.Is(err, ErrStaleProposal) {
		t.Fatalf("changed proposal decision error = %v", err)
	}
	decision := Decision{ID: "decision-ok", ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision, Approved: true}
	if _, err := broker.Decide(decision); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := broker.Decide(decision); err != nil || duplicate.ID != decision.ID {
		t.Fatalf("duplicate decision = %#v, %v", duplicate, err)
	}
	delete(broker.actionDecisions, proposal.ID)
	delete(broker.decisions, decision.ID)
	proposal.ExpiresAt = time.Now().Add(-time.Second)
	broker.proposals[proposal.ID] = proposal
	if _, err := broker.Decide(Decision{ID: "decision-2", ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision}); !errors.Is(err, ErrStaleProposal) {
		t.Fatalf("expired proposal decision error = %v", err)
	}
	proposal.ExpiresAt = time.Now().Add(time.Minute)
	proposal.Cancelled = true
	broker.proposals[proposal.ID] = proposal
	if _, err := broker.Decide(Decision{ID: "decision-3", ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision}); !errors.Is(err, ErrStaleProposal) {
		t.Fatalf("cancelled proposal decision error = %v", err)
	}
}

func TestBrokerWaitDecisionDeliversApprovedProposalDecision(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "change it"})
	proposal := Proposal{ID: "action-1", TurnID: turn.ID, Digest: "digest-1", DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute)}
	_, _ = broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal})
	want, err := broker.Decide(Decision{ID: "decision-1", ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := broker.WaitDecision(ctx, proposal.ID)
	if err != nil || got != want {
		t.Fatalf("WaitDecision() = %#v, %v", got, err)
	}
}

func TestBrokerConservativelyNormalizesUnknownProviderEvent(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "question"})
	got, err := broker.Publish(Event{
		ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: "provider_magic",
		Text: "Provider reported an unfamiliar state.", Diff: "must not become actionable",
		Proposal:    &Proposal{ID: "action-1", TurnID: turn.ID, Digest: "digest"},
		Interrupted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != EventProgress || got.Text != UnknownProviderEventText || got.Diff != "" || got.Proposal != nil || got.Interrupted {
		t.Fatalf("normalized event = %#v", got)
	}
	if _, ok := broker.proposals["action-1"]; ok {
		t.Fatal("unknown provider event created an actionable proposal")
	}
}

func TestBrokerRejectsChangedProposalWithReusedActionID(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "change it"})
	proposal := Proposal{
		ID: "action-1", TurnID: turn.ID, Digest: "digest-1", Scope: "first scope",
		DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute),
	}
	if _, err := broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal}); err != nil {
		t.Fatal(err)
	}
	changed := proposal
	changed.Scope = "different scope"
	if _, err := broker.Publish(Event{ID: "event-2", TurnID: turn.ID, Sequence: 2, Type: EventProposal, Proposal: &changed}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed proposal error = %v, want conflict", err)
	}
	if got := broker.proposals[proposal.ID]; !reflect.DeepEqual(got, proposal) {
		t.Fatalf("stored proposal changed: %#v", got)
	}
}

func TestBrokerNativeAuthorizationDenialCannotBecomeCompletion(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "change it"})
	proposal := Proposal{
		ID: "action-1", TurnID: turn.ID, Digest: "digest-1",
		DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute),
	}
	if _, err := broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal}); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Decide(Decision{
		ID: "decision-1", ActionID: proposal.ID, ProposalDigest: proposal.Digest,
		DocumentRevision: proposal.DocumentRevision, Approved: true,
	}); err != nil {
		t.Fatal(err)
	}
	denied, err := broker.Publish(Event{
		ID: "event-2", TurnID: turn.ID, Sequence: 2, Type: EventAuthorizationDenied,
		Text: "The provider denied filesystem authorization.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if denied.Type != EventAuthorizationDenied {
		t.Fatalf("denial = %#v", denied)
	}
	if _, err := broker.Publish(Event{ID: "event-3", TurnID: turn.ID, Sequence: 3, Type: EventCompleted}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("completion after native denial error = %v", err)
	}
}

func TestBrokerDecisionIsSingleUsePerActionAndRetryIsIdempotent(t *testing.T) {
	broker := NewBroker("attachment-1", "task-secret")
	turn, _ := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "question"})
	proposal := Proposal{ID: "action-1", TurnID: turn.ID, Digest: "digest-1", DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute)}
	_, _ = broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal})
	first := Decision{ID: "decision-1", ActionID: proposal.ID, ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision}
	if _, err := broker.Decide(first); err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.ID = "decision-retry"
	got, err := broker.Decide(retry)
	if err != nil || got.ID != first.ID {
		t.Fatalf("idempotent retry = %#v, %v", got, err)
	}
	changed := retry
	changed.ID = "decision-changed"
	changed.Approved = true
	if _, err := broker.Decide(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed decision error = %v, want conflict", err)
	}
	got, err = broker.WaitDecision(context.Background(), proposal.ID)
	if err != nil || got.Approved {
		t.Fatalf("wait returned %#v, %v; rejection must remain authoritative", got, err)
	}
}

func TestBrokerObserverCannotDecideProposal(t *testing.T) {
	broker := NewBrokerWithLeases("attachment-1", "task-secret", time.Minute, time.Minute)
	defer broker.Close()
	if err := broker.TaskHeartbeat("attachment-1"); err != nil {
		t.Fatal(err)
	}
	broker.BrowserHeartbeat("controller-1")
	broker.BrowserHeartbeat("observer-1")
	turn, err := broker.SubmitTurn(Turn{ID: "turn-1", ControllerID: "controller-1", Text: "question"})
	if err != nil {
		t.Fatal(err)
	}
	proposal := Proposal{ID: "action-1", TurnID: turn.ID, Digest: "digest-1", DocumentRevision: "rev-1", ExpiresAt: time.Now().Add(time.Minute)}
	_, _ = broker.Publish(Event{ID: "event-1", TurnID: turn.ID, Sequence: 1, Type: EventProposal, Proposal: &proposal})
	decision := Decision{
		ID: "decision-1", ActionID: proposal.ID, ControllerID: "observer-1",
		ProposalDigest: proposal.Digest, DocumentRevision: proposal.DocumentRevision, Approved: true,
	}
	if _, err := broker.Decide(decision); !errors.Is(err, ErrNotController) {
		t.Fatalf("observer decision error = %v, want not controller", err)
	}
	decision.ID = "decision-2"
	decision.ControllerID = "controller-1"
	if _, err := broker.Decide(decision); err != nil {
		t.Fatalf("controller decision: %v", err)
	}
}
