// Package webhooks dispatches signed HTTP callbacks for server events.
//
// Each hook is a URL with a shared secret and an optional event allowlist.
// Every delivery is a POST with the JSON body {"event","timestamp","payload"}
// and an HMAC-SHA256 signature over the raw body in X-Gopit-Signature (hex).
// Delivery is asynchronous and retried 3 times with linear backoff.
package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Event names (wire contract).
const (
	EventNodeUp         = "node.up"
	EventNodeDown       = "node.down"
	EventFirewallChange = "firewall.changed"
	EventDockerDeploy   = "docker.deployed"
	EventUserLogin      = "user.login"
	EventUserCreated    = "user.created"
	EventUserDeleted    = "user.deleted"
	EventPasswordChange = "password.changed"
)

// Events lists every valid event name.
var Events = []string{
	EventNodeUp, EventNodeDown, EventFirewallChange, EventDockerDeploy,
	EventUserLogin, EventUserCreated, EventUserDeleted, EventPasswordChange,
}

// Hook is one configured webhook endpoint.
type Hook struct {
	URL    string   `yaml:"url"`
	Secret string   `yaml:"secret"`
	Events []string `yaml:"events"` // empty = all events
}

// Store holds the configured hooks.
type Store struct {
	hooks  []Hook
	client *http.Client
}

// New builds the store. Hooks with an empty URL are dropped.
func New(hooks []Hook) *Store {
	filtered := hooks[:0:0]
	for _, h := range hooks {
		if h.URL != "" {
			filtered = append(filtered, h)
		}
	}
	return &Store{hooks: filtered, client: &http.Client{Timeout: 10 * time.Second}}
}

// Fire posts event+payload to every subscribed hook asynchronously with up
// to 3 attempts and linear backoff. Never blocks the caller.
func (s *Store) Fire(event string, payload any) {
	if len(s.hooks) == 0 {
		return
	}
	body, err := json.Marshal(map[string]any{
		"event": event, "timestamp": time.Now().UTC(), "payload": payload,
	})
	if err != nil {
		slog.Warn("webhook marshal failed", "event", event, "err", err)
		return
	}
	for _, h := range s.hooks {
		if !wants(h.Events, event) {
			continue
		}
		go s.dispatch(h, event, body)
	}
}

// wants reports whether a hook is subscribed to event.
func wants(events []string, event string) bool {
	if len(events) == 0 {
		return true // empty = all
	}
	for _, e := range events {
		if e == event {
			return true
		}
	}
	return false
}

// dispatch delivers one hook with 3 attempts and backoff, then gives up.
// ponytail: fire-and-forget goroutine, no queue; in-flight deliveries are
// dropped on process exit — add a persistent queue if delivery matters.
func (s *Store) dispatch(h Hook, event string, body []byte) {
	sig := Sign(h.Secret, body)
	for attempt := 1; ; attempt++ {
		if err := s.post(h, event, sig, body); err == nil {
			return
		} else if attempt < 3 {
			slog.Warn("webhook delivery failed, retrying", "url", h.URL, "event", event, "attempt", attempt, "err", err)
			time.Sleep(time.Duration(attempt) * time.Second)
		} else {
			slog.Warn("webhook delivery failed, giving up", "url", h.URL, "event", event, "err", err)
			return
		}
	}
}

func (s *Store) post(h Hook, event, sig string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gopit-Signature", sig)
	req.Header.Set("X-Gopit-Event", event)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("endpoint returned %s", resp.Status)
	}
	return nil
}

// Sign returns the hex HMAC-SHA256 of body with secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
