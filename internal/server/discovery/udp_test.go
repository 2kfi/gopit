package discovery

import (
	"encoding/json"
	"testing"

	"gopit/internal/protocol"
)

func TestParseAnnounce(t *testing.T) {
	info := protocol.NodeInfo{UUID: "u-1", Hostname: "box1", IP: "10.0.0.1", Port: 1221, OS: "linux", Arch: "amd64"}
	b, _ := json.Marshal(info)
	got, ok := ParseAnnounce("ANNOUNCE " + string(b))
	if !ok {
		t.Fatal("expected valid announce")
	}
	if got.UUID != "u-1" || got.Hostname != "box1" || got.Port != 1221 {
		t.Fatalf("parsed info mismatch: %+v", got)
	}

	if _, ok := ParseAnnounce("GOPIT DISCOVER"); ok {
		t.Fatal("DISCOVER must not parse as announce")
	}
	if _, ok := ParseAnnounce("ANNOUNCE not-json"); ok {
		t.Fatal("garbage announce must not parse")
	}
	if _, ok := ParseAnnounce(`ANNOUNCE {"hostname":"x"}`); ok {
		t.Fatal("announce without uuid must not parse")
	}
}
