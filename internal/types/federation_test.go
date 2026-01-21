package types

import (
	"strings"
	"testing"
	"time"
)

func TestFederatedMessageTypeIsValid(t *testing.T) {
	tests := []struct {
		msgType FederatedMessageType
		valid   bool
	}{
		{MsgWorkHandoff, true},
		{MsgQuery, true},
		{MsgReply, true},
		{MsgBroadcast, true},
		{MsgAck, true},
		{MsgReject, true},
		{FederatedMessageType(""), false},
		{FederatedMessageType("invalid"), false},
		{FederatedMessageType("work_handoff"), false}, // underscore not dash
	}

	for _, tt := range tests {
		t.Run(string(tt.msgType), func(t *testing.T) {
			if got := tt.msgType.IsValid(); got != tt.valid {
				t.Errorf("FederatedMessageType(%q).IsValid() = %v, want %v", tt.msgType, got, tt.valid)
			}
		})
	}
}

func TestFederatedMessageTypeIsResponse(t *testing.T) {
	tests := []struct {
		msgType    FederatedMessageType
		isResponse bool
	}{
		{MsgWorkHandoff, false},
		{MsgQuery, false},
		{MsgReply, true},
		{MsgBroadcast, false},
		{MsgAck, true},
		{MsgReject, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.msgType), func(t *testing.T) {
			if got := tt.msgType.IsResponse(); got != tt.isResponse {
				t.Errorf("FederatedMessageType(%q).IsResponse() = %v, want %v", tt.msgType, got, tt.isResponse)
			}
		})
	}
}

func TestFederatedMessageTypeRequiresRecipient(t *testing.T) {
	tests := []struct {
		msgType          FederatedMessageType
		requiresRecipient bool
	}{
		{MsgWorkHandoff, true},
		{MsgQuery, true},
		{MsgReply, true},
		{MsgBroadcast, false}, // broadcasts don't require specific recipient
		{MsgAck, true},
		{MsgReject, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.msgType), func(t *testing.T) {
			if got := tt.msgType.RequiresRecipient(); got != tt.requiresRecipient {
				t.Errorf("FederatedMessageType(%q).RequiresRecipient() = %v, want %v", tt.msgType, got, tt.requiresRecipient)
			}
		})
	}
}

func TestRejectCodeIsValid(t *testing.T) {
	tests := []struct {
		code  RejectCode
		valid bool
	}{
		{RejectInvalid, true},
		{RejectUnauthorized, true},
		{RejectCapacity, true},
		{RejectNotFound, true},
		{RejectTimeout, true},
		{RejectDuplicate, true},
		{RejectCode(""), false},
		{RejectCode("unknown"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			if got := tt.code.IsValid(); got != tt.valid {
				t.Errorf("RejectCode(%q).IsValid() = %v, want %v", tt.code, got, tt.valid)
			}
		})
	}
}

func TestFederatedMessageValidation(t *testing.T) {
	validSender := &EntityRef{
		Name:     "town-alpha",
		Platform: "gastown",
		Org:      "acme",
		ID:       "town-alpha",
	}

	tests := []struct {
		name    string
		msg     FederatedMessage
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid work-handoff message",
			msg: FederatedMessage{
				ID:        "msg-001",
				Type:      MsgWorkHandoff,
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: false,
		},
		{
			name: "valid query message",
			msg: FederatedMessage{
				ID:        "msg-002",
				Type:      MsgQuery,
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: false,
		},
		{
			name: "valid broadcast message",
			msg: FederatedMessage{
				ID:        "msg-003",
				Type:      MsgBroadcast,
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: false,
		},
		{
			name: "valid reply message with reply_to",
			msg: FederatedMessage{
				ID:        "msg-004",
				Type:      MsgReply,
				Timestamp: time.Now(),
				Sender:    validSender,
				ReplyTo:   "msg-002",
			},
			wantErr: false,
		},
		{
			name: "valid ack message with reply_to",
			msg: FederatedMessage{
				ID:        "msg-005",
				Type:      MsgAck,
				Timestamp: time.Now(),
				Sender:    validSender,
				ReplyTo:   "msg-001",
			},
			wantErr: false,
		},
		{
			name: "valid reject message with reply_to",
			msg: FederatedMessage{
				ID:         "msg-006",
				Type:       MsgReject,
				Timestamp:  time.Now(),
				Sender:     validSender,
				ReplyTo:    "msg-001",
				RejectCode: RejectCapacity,
			},
			wantErr: false,
		},
		{
			name: "missing ID",
			msg: FederatedMessage{
				Type:      MsgWorkHandoff,
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: true,
			errMsg:  "message ID is required",
		},
		{
			name: "invalid message type",
			msg: FederatedMessage{
				ID:        "msg-007",
				Type:      FederatedMessageType("invalid"),
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: true,
			errMsg:  "invalid message type",
		},
		{
			name: "missing timestamp",
			msg: FederatedMessage{
				ID:     "msg-008",
				Type:   MsgWorkHandoff,
				Sender: validSender,
			},
			wantErr: true,
			errMsg:  "timestamp is required",
		},
		{
			name: "missing sender",
			msg: FederatedMessage{
				ID:        "msg-009",
				Type:      MsgWorkHandoff,
				Timestamp: time.Now(),
			},
			wantErr: true,
			errMsg:  "sender is required",
		},
		{
			name: "empty sender",
			msg: FederatedMessage{
				ID:        "msg-010",
				Type:      MsgWorkHandoff,
				Timestamp: time.Now(),
				Sender:    &EntityRef{},
			},
			wantErr: true,
			errMsg:  "sender is required",
		},
		{
			name: "reply without reply_to",
			msg: FederatedMessage{
				ID:        "msg-014",
				Type:      MsgReply,
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: true,
			errMsg:  "reply message requires reply_to field",
		},
		{
			name: "ack without reply_to",
			msg: FederatedMessage{
				ID:        "msg-015",
				Type:      MsgAck,
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: true,
			errMsg:  "ack message requires reply_to field",
		},
		{
			name: "reject without reply_to",
			msg: FederatedMessage{
				ID:        "msg-016",
				Type:      MsgReject,
				Timestamp: time.Now(),
				Sender:    validSender,
			},
			wantErr: true,
			errMsg:  "reject message requires reply_to field",
		},
		{
			name: "reject with invalid reject code",
			msg: FederatedMessage{
				ID:         "msg-017",
				Type:       MsgReject,
				Timestamp:  time.Now(),
				Sender:     validSender,
				ReplyTo:    "msg-001",
				RejectCode: RejectCode("unknown"),
			},
			wantErr: true,
			errMsg:  "invalid reject code",
		},
		{
			name: "valid message with signature",
			msg: FederatedMessage{
				ID:          "msg-019",
				Type:        MsgWorkHandoff,
				Timestamp:   time.Now(),
				Sender:      validSender,
				Signature:   "ed25519:ABC123...",
				SignerKeyID: "key-001",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("FederatedMessage.Validate() expected error containing %q, got nil", tt.errMsg)
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("FederatedMessage.Validate() error = %q, want error containing %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("FederatedMessage.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestFederatedMessageTypeString(t *testing.T) {
	tests := []struct {
		msgType FederatedMessageType
		want    string
	}{
		{MsgWorkHandoff, "work-handoff"},
		{MsgQuery, "query"},
		{MsgReply, "reply"},
		{MsgBroadcast, "broadcast"},
		{MsgAck, "ack"},
		{MsgReject, "reject"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.msgType.String(); got != tt.want {
				t.Errorf("FederatedMessageType.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRejectCodeString(t *testing.T) {
	tests := []struct {
		code RejectCode
		want string
	}{
		{RejectInvalid, "invalid"},
		{RejectUnauthorized, "unauthorized"},
		{RejectCapacity, "capacity"},
		{RejectNotFound, "not_found"},
		{RejectTimeout, "timeout"},
		{RejectDuplicate, "duplicate"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.code.String(); got != tt.want {
				t.Errorf("RejectCode.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

