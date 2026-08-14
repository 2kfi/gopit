package protocol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Envelope is the wire format for every message between server and agent.
type Envelope struct {
	Type    string          `json:"type"`              // request | response | event
	ID      string          `json:"id"`                // uuid; echoed by responses
	Method  string          `json:"method"`            // auth | system.info | system.stats | ...
	Payload json.RawMessage `json:"payload,omitempty"` // method-specific object
	Error   *string         `json:"error,omitempty"`   // null on success
}

// Message types.
const (
	TypeRequest  = "request"
	TypeResponse = "response"
	TypeEvent    = "event"
)

// Terminal methods.
const (
	MethodTerminalOpen   = "terminal.open"
	MethodTerminalResize = "terminal.resize"
	MethodTerminalClose  = "terminal.close"
	MethodTerminalExit   = "terminal.exit"
)

// NewRequest builds a request envelope with a fresh UUID.
func NewRequest(method string, payload any) Envelope {
	return Envelope{
		Type:    TypeRequest,
		ID:      uuid.NewString(),
		Method:  method,
		Payload: mustMarshal(payload),
	}
}

// NewResponse builds a success response echoing the request id.
func NewResponse(id string, payload any) Envelope {
	return Envelope{
		Type:    TypeResponse,
		ID:      id,
		Method:  "",
		Payload: mustMarshal(payload),
		Error:   nil,
	}
}

// NewErrorResponse builds an error response echoing the request id.
func NewErrorResponse(id, errMsg string) Envelope {
	return Envelope{
		Type:  TypeResponse,
		ID:    id,
		Error: &errMsg,
	}
}

// NewEvent builds a fire-and-forget event (no id required).
func NewEvent(method string, payload any) Envelope {
	return Envelope{
		Type:    TypeEvent,
		ID:      uuid.NewString(),
		Method:  method,
		Payload: mustMarshal(payload),
	}
}

// Decode unmarshals a response envelope's payload into out, returning the error field.
func (e Envelope) Decode(out any) error {
	if e.Error != nil && *e.Error != "" {
		return &ErrRemote{Msg: *e.Error}
	}
	return json.Unmarshal(e.Payload, out)
}

// ErrRemote is an error reported by the peer inside an envelope.
type ErrRemote struct{ Msg string }

func (e *ErrRemote) Error() string { return "remote error: " + e.Msg }

func mustMarshal(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		// Constructors are always called with marshalable values (structs/maps).
		panic("protocol: cannot marshal payload: " + err.Error())
	}
	return b
}

// WaitForID blocks until an envelope with the given id arrives on ch, or times out.
func WaitForID(ch <-chan Envelope, id string, timeout time.Duration) (Envelope, bool) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return Envelope{}, false
			}
			if e.ID == id {
				return e, true
			}
		case <-t.C:
			return Envelope{}, false
		}
	}
}
