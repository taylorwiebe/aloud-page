package agentbridge

import "time"

const ProtocolVersion = "v1"

type Attachment struct {
	Version string `json:"version"`
	ID      string `json:"id"`
}

type Descriptor struct {
	Version      string `json:"version"`
	AttachmentID string `json:"attachment_id"`
	Endpoint     string `json:"endpoint"`
	TaskSecret   string `json:"task_secret"`
}

type Selection struct {
	Text                      string   `json:"text"`
	Representation            string   `json:"representation"`
	SectionID                 string   `json:"section_id,omitempty"`
	BlockIndex                int      `json:"block_index,omitempty"`
	Start                     int      `json:"start,omitempty"`
	End                       int      `json:"end,omitempty"`
	Revision                  string   `json:"revision,omitempty"`
	CandidateSourceSectionIDs []string `json:"candidate_source_section_ids,omitempty"`
}

type Turn struct {
	ID               string     `json:"id"`
	ControllerID     string     `json:"controller_id"`
	Text             string     `json:"text"`
	DocumentRevision string     `json:"document_revision,omitempty"`
	Selection        *Selection `json:"selection,omitempty"`
}

type EventType string

const (
	EventProgress     EventType = "progress"
	EventText         EventType = "text"
	EventProposal     EventType = "proposal"
	EventCompleted    EventType = "completed"
	EventFailed       EventType = "failed"
	EventCancelled    EventType = "cancelled"
	EventDisconnected EventType = "disconnected"
)

type Proposal struct {
	ID               string    `json:"id"`
	TurnID           string    `json:"turn_id"`
	Digest           string    `json:"digest"`
	Scope            string    `json:"scope,omitempty"`
	DocumentRevision string    `json:"document_revision"`
	ExpiresAt        time.Time `json:"expires_at"`
	Cancelled        bool      `json:"cancelled,omitempty"`
}

type Decision struct {
	ID               string `json:"id"`
	ActionID         string `json:"action_id"`
	ProposalDigest   string `json:"proposal_digest"`
	DocumentRevision string `json:"document_revision"`
	Approved         bool   `json:"approved"`
}

type Event struct {
	ID          string    `json:"id"`
	TurnID      string    `json:"turn_id"`
	Sequence    uint64    `json:"sequence"`
	Type        EventType `json:"type"`
	Text        string    `json:"text,omitempty"`
	Diff        string    `json:"diff,omitempty"`
	Proposal    *Proposal `json:"proposal,omitempty"`
	Interrupted bool      `json:"interrupted,omitempty"`
}

type CapabilityState struct {
	Capabilities []Capability `json:"capabilities"`
}

type Lease struct {
	AttachmentID string    `json:"attachment_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}
