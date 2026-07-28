package agentbridge

import (
	"context"
	"errors"
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
