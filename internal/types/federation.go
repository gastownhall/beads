// Package types defines core data structures for the bd issue tracker.
package types

import (
	"fmt"
	"time"
)

// FederatedMessageType categorizes inter-town communication.
type FederatedMessageType string

// Federated message type constants
const (
	// MsgWorkHandoff delegates work from one town to another.
	MsgWorkHandoff FederatedMessageType = "work-handoff"

	// MsgQuery requests information from a peer town.
	// Used for issue lookups, status checks, and capability discovery.
	MsgQuery FederatedMessageType = "query"

	// MsgReply responds to a query message.
	// Contains the requested data or an error explanation.
	MsgReply FederatedMessageType = "reply"

	// MsgBroadcast announces information to all peer towns.
	// Used for status updates, capability changes, and town-wide notifications.
	MsgBroadcast FederatedMessageType = "broadcast"

	// MsgAck acknowledges receipt and acceptance of a message.
	MsgAck FederatedMessageType = "ack"

	// MsgReject indicates a message was received but rejected.
	// Includes reason for rejection (invalid, unauthorized, capacity, etc.)
	MsgReject FederatedMessageType = "reject"
)

// validMessageTypes is the set of allowed message type values
var validMessageTypes = map[FederatedMessageType]bool{
	MsgWorkHandoff: true,
	MsgQuery:       true,
	MsgReply:       true,
	MsgBroadcast:   true,
	MsgAck:         true,
	MsgReject:      true,
}

// IsValid checks if the message type value is valid.
func (t FederatedMessageType) IsValid() bool {
	return validMessageTypes[t]
}

// String returns the string representation of the message type.
func (t FederatedMessageType) String() string {
	return string(t)
}

// FederatedMessage represents a message exchanged between federated towns.
// Messages are cryptographically signed for authenticity verification.
type FederatedMessage struct {
	// ===== Core Identity =====

	// ID is a unique identifier for this message (UUID or similar).
	ID string `json:"id"`

	// Type categorizes the message purpose.
	Type FederatedMessageType `json:"type"`

	// Timestamp is when the message was created.
	Timestamp time.Time `json:"timestamp"`

	// ===== Routing =====

	// Sender identifies the originating town/entity.
	Sender *EntityRef `json:"sender"`

	// Recipient identifies the target town/entity (nil for broadcasts).
	Recipient *EntityRef `json:"recipient,omitempty"`

	// ReplyTo links this message to a previous message (for replies, acks, rejects).
	ReplyTo string `json:"reply_to,omitempty"`

	// ===== Payload =====

	// Subject is a brief description of the message content.
	Subject string `json:"subject,omitempty"`

	// Payload contains the message-specific data (JSON-encoded).
	// For work-handoff: issue data
	// For query: query parameters
	// For reply: response data
	// For broadcast: announcement data
	// For ack/reject: processing result
	Payload string `json:"payload,omitempty"`

	// BeadID references a specific bead if applicable.
	BeadID string `json:"bead_id,omitempty"`

	// ===== Security =====

	// Signature is the cryptographic signature of the message content.
	// Format: "<algorithm>:<base64-encoded-signature>"
	// Example: "ed25519:ABC123..."
	Signature string `json:"signature,omitempty"`

	// SignerKeyID identifies which key was used to sign (for key rotation).
	SignerKeyID string `json:"signer_key_id,omitempty"`

	// ===== Rejection Details (for MsgReject) =====

	// RejectReason explains why a message was rejected.
	RejectReason string `json:"reject_reason,omitempty"`

	// RejectCode is a machine-readable rejection code.
	RejectCode RejectCode `json:"reject_code,omitempty"`
}

// RejectCode categorizes rejection reasons for machine processing.
type RejectCode string

// Rejection code constants
const (
	// RejectInvalid indicates the message format or content is invalid.
	RejectInvalid RejectCode = "invalid"

	// RejectUnauthorized indicates the sender lacks permission.
	RejectUnauthorized RejectCode = "unauthorized"

	// RejectCapacity indicates the recipient is at capacity.
	RejectCapacity RejectCode = "capacity"

	// RejectNotFound indicates the referenced resource doesn't exist.
	RejectNotFound RejectCode = "not_found"

	// RejectTimeout indicates the message or referenced work has expired.
	RejectTimeout RejectCode = "timeout"

	// RejectDuplicate indicates this message was already processed.
	RejectDuplicate RejectCode = "duplicate"
)

// validRejectCodes is the set of allowed reject code values
var validRejectCodes = map[RejectCode]bool{
	RejectInvalid:      true,
	RejectUnauthorized: true,
	RejectCapacity:     true,
	RejectNotFound:     true,
	RejectTimeout:      true,
	RejectDuplicate:    true,
}

// IsValid checks if the reject code value is valid.
func (c RejectCode) IsValid() bool {
	return validRejectCodes[c]
}

// String returns the string representation of the reject code.
func (c RejectCode) String() string {
	return string(c)
}

// Validate checks if the FederatedMessage has valid field values.
func (m *FederatedMessage) Validate() error {
	if m.ID == "" {
		return fmt.Errorf("message ID is required")
	}
	if !m.Type.IsValid() {
		return fmt.Errorf("invalid message type: %s", m.Type)
	}
	if m.Timestamp.IsZero() {
		return fmt.Errorf("timestamp is required")
	}
	if m.Sender == nil || m.Sender.IsEmpty() {
		return fmt.Errorf("sender is required")
	}

	// Type-specific validation
	switch m.Type {
	case MsgReply, MsgAck, MsgReject:
		if m.ReplyTo == "" {
			return fmt.Errorf("%s message requires reply_to field", m.Type)
		}
	}

	// Additional validation for reject messages
	if m.Type == MsgReject && m.RejectCode != "" && !m.RejectCode.IsValid() {
		return fmt.Errorf("invalid reject code: %s", m.RejectCode)
	}

	return nil
}

// IsResponse returns true if this message type is a response to another message.
func (t FederatedMessageType) IsResponse() bool {
	return t == MsgReply || t == MsgAck || t == MsgReject
}

// RequiresRecipient returns true if this message type requires a specific recipient.
func (t FederatedMessageType) RequiresRecipient() bool {
	// Broadcasts don't require a specific recipient
	return t != MsgBroadcast
}

// WorkHandoffPayload is the payload structure for work-handoff messages.
// Embedded in FederatedMessage.Payload as JSON.
type WorkHandoffPayload struct {
	// Issue is the issue being handed off.
	Issue *Issue `json:"issue"`

	// Labels are the issue's labels.
	Labels []string `json:"labels,omitempty"`

	// Dependencies are the issue's dependencies.
	Dependencies []*Dependency `json:"dependencies,omitempty"`

	// Reason explains why the work is being handed off.
	Reason string `json:"reason,omitempty"`

	// Deadline is when the work should be completed.
	Deadline *time.Time `json:"deadline,omitempty"`

	// Priority override for the receiving town (optional).
	PriorityOverride *int `json:"priority_override,omitempty"`
}

// QueryPayload is the payload structure for query messages.
type QueryPayload struct {
	// QueryType identifies the kind of query.
	// Examples: "issue-status", "capability-check", "bead-lookup"
	QueryType string `json:"query_type"`

	// Parameters contains query-specific parameters.
	Parameters map[string]string `json:"parameters,omitempty"`
}

// ReplyPayload is the payload structure for reply messages.
type ReplyPayload struct {
	// Success indicates whether the query was successful.
	Success bool `json:"success"`

	// Data contains the query response (type depends on query).
	Data string `json:"data,omitempty"`

	// Error contains error details if Success is false.
	Error string `json:"error,omitempty"`
}

// AckPayload is the payload structure for acknowledgment messages.
type AckPayload struct {
	// Accepted indicates whether the message was accepted for processing.
	Accepted bool `json:"accepted"`

	// ProcessingID is an optional ID for tracking the processing.
	ProcessingID string `json:"processing_id,omitempty"`

	// EstimatedCompletion is when the work is expected to complete.
	EstimatedCompletion *time.Time `json:"estimated_completion,omitempty"`
}

// BroadcastPayload is the payload structure for broadcast messages.
type BroadcastPayload struct {
	// BroadcastType categorizes the broadcast.
	// Examples: "status-update", "capability-change", "town-announcement"
	BroadcastType string `json:"broadcast_type"`

	// Message is the broadcast content.
	Message string `json:"message"`

	// Metadata contains additional broadcast-specific data.
	Metadata map[string]string `json:"metadata,omitempty"`
}
