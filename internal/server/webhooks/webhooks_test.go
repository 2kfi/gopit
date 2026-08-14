package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSign(t *testing.T) {
	secret, body := "s3cret", []byte(`{"event":"node.up"}`)
	got := Sign(secret, body)
	want := func() string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		return hex.EncodeToString(mac.Sum(nil))
	}()
	if got != want {
		t.Fatalf("Sign mismatch:\n got %s\nwant %s", got, want)
	}
	if got == Sign("other", body) {
		t.Fatal("different secret must produce a different signature")
	}
}

func TestWants(t *testing.T) {
	if !wants(nil, EventNodeUp) {
		t.Fatal("empty events must subscribe to everything")
	}
	if !wants([]string{EventNodeUp, EventNodeDown}, EventNodeDown) {
		t.Fatal("listed event must match")
	}
	if wants([]string{EventUserLogin}, EventNodeUp) {
		t.Fatal("unlisted event must not match")
	}
}
