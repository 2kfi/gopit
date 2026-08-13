package protocol

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	req := NewRequest("system.info", NodeInfo{UUID: "abc", Hostname: "box1"})
	if req.Type != TypeRequest || req.ID == "" || req.Method != "system.info" {
		t.Fatalf("bad request envelope: %+v", req)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != TypeRequest || back.ID != req.ID || back.Method != req.Method {
		t.Fatalf("round trip mismatch: %+v", back)
	}
	var info NodeInfo
	if err := json.Unmarshal(back.Payload, &info); err != nil {
		t.Fatal(err)
	}
	if info.Hostname != "box1" || info.UUID != "abc" {
		t.Fatalf("payload mismatch: %+v", info)
	}

	resp := NewResponse(req.ID, SystemStats{Mem: MemStats{Total: 100}})
	if resp.Type != TypeResponse || resp.ID != req.ID || resp.Error != nil {
		t.Fatalf("bad response envelope: %+v", resp)
	}
	var stats SystemStats
	if err := resp.Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.Mem.Total != 100 {
		t.Fatalf("decoded stats mismatch: %+v", stats)
	}

	errResp := NewErrorResponse(req.ID, "nope")
	var out struct{}
	if err := errResp.Decode(&out); err == nil {
		t.Fatal("expected error on error envelope decode")
	}
}
