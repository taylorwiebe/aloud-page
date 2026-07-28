package agentbridge

import (
	"context"
	"crypto/subtle"
	"errors"
	"reflect"
	"sync"
	"time"
)

var (
	ErrAttachment    = errors.New("attachment identity does not match")
	ErrBusy          = errors.New("a turn is already active")
	ErrConflict      = errors.New("identifier was already used with different content")
	ErrInvalidState  = errors.New("invalid conversation state transition")
	ErrClosed        = errors.New("conversation attachment is closed")
	ErrDisconnected  = errors.New("originating task is disconnected; reconnect the same attachment")
	ErrNotController = errors.New("only the elected controller may submit")
	ErrSequence      = errors.New("event sequence is not monotonic")
	ErrStaleProposal = errors.New("proposal is missing, changed, expired, cancelled, or stale")
)

const maxJournalEvents = 1024

type Broker struct {
	mu           sync.Mutex
	changed      chan struct{}
	attachmentID string
	taskSecret   string
	controllerID string
	active       *Turn
	turns        map[string]Turn
	events       []Event
	eventIDs     map[string]Event
	decisions    map[string]Decision
	proposals    map[string]Proposal
	disconnected bool
	closed       bool
	leases       *LeaseManager
}

func NewBroker(attachmentID, taskSecret string) *Broker {
	return &Broker{
		changed:      make(chan struct{}),
		attachmentID: attachmentID,
		taskSecret:   taskSecret,
		turns:        make(map[string]Turn),
		eventIDs:     make(map[string]Event),
		decisions:    make(map[string]Decision),
		proposals:    make(map[string]Proposal),
	}
}

func NewBrokerWithLeases(attachmentID, taskSecret string, reloadGrace, taskTTL time.Duration) *Broker {
	broker := NewBroker(attachmentID, taskSecret)
	broker.leases = NewLeaseManager(attachmentID, reloadGrace, taskTTL)
	return broker
}

func (b *Broker) Attachment() Attachment {
	return Attachment{Version: ProtocolVersion, ID: b.attachmentID}
}

func (b *Broker) AuthorizeTask(secret string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(b.taskSecret)) == 1
}

func (b *Broker) SubmitTurn(turn Turn) (Turn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Turn{}, ErrClosed
	}
	if b.leases != nil && !b.leases.TaskConnected() {
		return Turn{}, ErrDisconnected
	}
	if existing, ok := b.turns[turn.ID]; ok {
		if reflect.DeepEqual(existing, turn) {
			return existing, nil
		}
		return Turn{}, ErrConflict
	}
	if turn.ID == "" || turn.ControllerID == "" || turn.Text == "" {
		return Turn{}, ErrInvalidState
	}
	if b.controllerID != "" && b.controllerID != turn.ControllerID {
		return Turn{}, ErrNotController
	}
	if b.active != nil {
		return Turn{}, ErrBusy
	}
	b.controllerID = turn.ControllerID
	b.turns[turn.ID] = turn
	copy := turn
	b.active = &copy
	b.disconnected = false
	b.signalLocked()
	return turn, nil
}

func (b *Broker) WaitTurn(ctx context.Context, attachmentID string) (Turn, error) {
	if attachmentID != "" && attachmentID != b.attachmentID {
		return Turn{}, ErrAttachment
	}
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return Turn{}, ErrClosed
		}
		if b.active != nil {
			turn := *b.active
			b.mu.Unlock()
			return turn, nil
		}
		wait := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Turn{}, ctx.Err()
		case <-wait:
		}
	}
}

func (b *Broker) Publish(event Event) (Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Event{}, ErrClosed
	}
	if existing, ok := b.eventIDs[event.ID]; ok {
		if reflect.DeepEqual(existing, event) {
			return existing, nil
		}
		return Event{}, ErrConflict
	}
	if b.active == nil || event.TurnID != b.active.ID || event.ID == "" {
		return Event{}, ErrInvalidState
	}
	event = normalizeProviderEvent(event)
	expected := uint64(1)
	if len(b.events) > 0 {
		expected = b.events[len(b.events)-1].Sequence + 1
	}
	if event.Sequence != expected {
		return Event{}, ErrSequence
	}
	if event.Type == EventProposal {
		if event.Proposal == nil || event.Proposal.ID == "" || event.Proposal.TurnID != event.TurnID || event.Proposal.Digest == "" {
			return Event{}, ErrInvalidState
		}
		if event.Proposal.ChangesPlan && (event.Proposal.BaseSourceDigest == "" || event.Proposal.SourceDiff == "" ||
			event.Proposal.Scope == "" || event.Proposal.FriendlyEffect == "") {
			return Event{}, ErrInvalidState
		}
		b.proposals[event.Proposal.ID] = *event.Proposal
	}
	b.appendEventLocked(event)
	b.eventIDs[event.ID] = event
	if terminalEvent(event.Type) {
		b.active = nil
	}
	b.signalLocked()
	return event, nil
}

func (b *Broker) Decide(decision Decision) (Decision, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Decision{}, ErrClosed
	}
	if existing, ok := b.decisions[decision.ID]; ok {
		if reflect.DeepEqual(existing, decision) {
			return existing, nil
		}
		return Decision{}, ErrConflict
	}
	proposal, ok := b.proposals[decision.ActionID]
	if !ok || proposal.Cancelled || proposal.ExpiresAt.IsZero() || !time.Now().Before(proposal.ExpiresAt) ||
		proposal.Digest != decision.ProposalDigest || proposal.DocumentRevision != decision.DocumentRevision ||
		b.active == nil || b.active.ID != proposal.TurnID {
		return Decision{}, ErrStaleProposal
	}
	if decision.ID == "" {
		return Decision{}, ErrInvalidState
	}
	b.decisions[decision.ID] = decision
	b.signalLocked()
	return decision, nil
}

func (b *Broker) WaitDecision(ctx context.Context, actionID string) (Decision, error) {
	if actionID == "" {
		return Decision{}, ErrInvalidState
	}
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return Decision{}, ErrClosed
		}
		for _, decision := range b.decisions {
			if decision.ActionID == actionID {
				b.mu.Unlock()
				return decision, nil
			}
		}
		if _, ok := b.proposals[actionID]; !ok {
			b.mu.Unlock()
			return Decision{}, ErrStaleProposal
		}
		wait := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Decision{}, ctx.Err()
		case <-wait:
		}
	}
}

func (b *Broker) TaskHeartbeat(attachmentID string) error {
	if b.leases == nil {
		return nil
	}
	err := b.leases.TaskHeartbeat(attachmentID)
	if err == nil {
		b.mu.Lock()
		b.disconnected = false
		b.mu.Unlock()
	}
	return err
}

func (b *Broker) BrowserHeartbeat(browserID string) BrowserRole {
	if b.leases == nil {
		return BrowserController
	}
	role := b.leases.BrowserHeartbeat(browserID)
	if role == BrowserController {
		b.mu.Lock()
		if b.active == nil {
			b.controllerID = browserID
		}
		b.mu.Unlock()
	}
	return role
}

func (b *Broker) BrowserRelease(browserID string) {
	if b.leases != nil {
		b.leases.BrowserRelease(browserID)
	}
}

func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	for id, proposal := range b.proposals {
		proposal.Cancelled = true
		b.proposals[id] = proposal
	}
	if b.leases != nil {
		b.leases.Close()
	}
	b.signalLocked()
	b.mu.Unlock()
}

func (b *Broker) Replay(after uint64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]Event, 0)
	for _, event := range b.events {
		if event.Sequence > after {
			result = append(result, event)
		}
	}
	return result
}

func (b *Broker) Disconnect() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disconnected {
		return
	}
	b.disconnected = true
	if b.active != nil {
		sequence := uint64(1)
		if len(b.events) > 0 {
			sequence = b.events[len(b.events)-1].Sequence + 1
		}
		event := Event{
			ID:          "disconnect-" + b.active.ID,
			TurnID:      b.active.ID,
			Sequence:    sequence,
			Type:        EventDisconnected,
			Text:        "The originating task disconnected.",
			Interrupted: true,
		}
		b.appendEventLocked(event)
		b.eventIDs[event.ID] = event
	}
	b.signalLocked()
}

func (b *Broker) appendEventLocked(event Event) {
	b.events = append(b.events, event)
	if len(b.events) > maxJournalEvents {
		for _, removed := range b.events[:len(b.events)-maxJournalEvents] {
			delete(b.eventIDs, removed.ID)
		}
		b.events = append([]Event(nil), b.events[len(b.events)-maxJournalEvents:]...)
	}
}

func (b *Broker) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func terminalEvent(eventType EventType) bool {
	return eventType == EventCompleted || eventType == EventFailed || eventType == EventCancelled ||
		eventType == EventAuthorizationDenied
}

func normalizeProviderEvent(event Event) Event {
	switch event.Type {
	case EventProgress, EventText, EventProposal, EventCompleted, EventFailed, EventCancelled, EventDisconnected, EventAuthorizationDenied:
		return event
	default:
		event.Type = EventProgress
		event.Text = UnknownProviderEventText
		event.Diff = ""
		event.Proposal = nil
		event.Interrupted = false
		return event
	}
}
