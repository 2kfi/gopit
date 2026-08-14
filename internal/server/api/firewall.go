package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gopit/internal/server/nodemanager"
	"gopit/internal/server/webhooks"
)

// FirewallAPI proxies ufw.* requests from the browser to the node's agent.
type FirewallAPI struct {
	manager *nodemanager.Manager
	hooks   *webhooks.Store
}

// NewFirewallAPI wires the firewall routes.
func NewFirewallAPI(m *nodemanager.Manager, hooks *webhooks.Store) *FirewallAPI {
	return &FirewallAPI{manager: m, hooks: hooks}
}

// proxyRequest runs one request against the node's agent and forwards the
// payload. Agent offline -> 503; agent error/timeout -> 502 with its message.
// Returns nil when the request succeeded (for post-success hooks).
func proxyRequest(m *nodemanager.Manager, w http.ResponseWriter, nodeID, method string, payload any) error {
	conn := m.Conn(nodeID)
	if conn == nil {
		writeErr(w, http.StatusServiceUnavailable, "node offline")
		return errNodeOffline
	}
	resp, err := conn.Request(method, payload, dockerRequestTimeout)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return err
	}
	if resp.Error != nil && *resp.Error != "" {
		writeErr(w, http.StatusBadGateway, *resp.Error)
		return errAgentRejected
	}
	writeJSON(w, http.StatusOK, resp.Payload)
	return nil
}

var (
	errNodeOffline   = &proxyError{"node offline"}
	errAgentRejected = &proxyError{"agent rejected"}
)

type proxyError struct{ msg string }

func (e *proxyError) Error() string { return e.msg }

// ruleAddBody is the wire contract of an add-rule (and preview) request.
type ruleAddBody struct {
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	Action    string `json:"action"`
	From      string `json:"from"`
	Interface string `json:"interface"`
}

// parseRuleAdd decodes and validates a rule-add body.
func parseRuleAdd(w http.ResponseWriter, r *http.Request) (*ruleAddBody, bool) {
	var body ruleAddBody
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return nil, false
	}
	if body.Port < 1 || body.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "port must be 1-65535")
		return nil, false
	}
	switch body.Protocol {
	case "tcp", "udp", "both":
	default:
		writeErr(w, http.StatusBadRequest, "protocol must be tcp, udp or both")
		return nil, false
	}
	switch body.Action {
	case "allow", "deny", "reject":
	default:
		writeErr(w, http.StatusBadRequest, "action must be allow, deny or reject")
		return nil, false
	}
	if body.From != "" {
		if _, _, err := net.ParseCIDR(body.From); err != nil {
			if ip := net.ParseIP(body.From); ip == nil {
				writeErr(w, http.StatusBadRequest, "from must be a valid IP or CIDR")
				return nil, false
			}
		}
	}
	return &body, true
}

// Status returns the live ufw status, default policies and rule table.
func (a *FirewallAPI) Status(w http.ResponseWriter, r *http.Request) {
	proxyRequest(a.manager, w, chi.URLParam(r, "uuid"), "ufw.status", nil)
}

// Preview simulates adding a rule on the agent and returns the full firewall
// state as it would look AFTER the change, without applying anything.
func (a *FirewallAPI) Preview(w http.ResponseWriter, r *http.Request) {
	body, ok := parseRuleAdd(w, r)
	if !ok {
		return
	}
	proxyRequest(a.manager, w, chi.URLParam(r, "uuid"), "ufw.rule.preview", body)
}

// AddRule adds one allow/deny/reject rule for port and protocol.
func (a *FirewallAPI) AddRule(w http.ResponseWriter, r *http.Request) {
	body, ok := parseRuleAdd(w, r)
	if !ok {
		return
	}
	if err := proxyRequest(a.manager, w, chi.URLParam(r, "uuid"), "ufw.rule.add", body); err != nil {
		return
	}
	a.hooks.Fire(webhooks.EventFirewallChange, map[string]any{
		"node_id": chi.URLParam(r, "uuid"), "action": "add",
		"rule": map[string]any{"protocol": body.Protocol, "port": body.Port, "action": body.Action, "from": body.From},
	})
}

// DeleteRule deletes a rule by its ufw numbering.
func (a *FirewallAPI) DeleteRule(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil || n < 1 {
		writeErr(w, http.StatusBadRequest, "invalid rule number")
		return
	}
	uuid := chi.URLParam(r, "uuid")
	if err := proxyRequest(a.manager, w, uuid, "ufw.rule.delete", map[string]int{"number": n}); err != nil {
		return
	}
	a.hooks.Fire(webhooks.EventFirewallChange, map[string]any{"node_id": uuid, "action": "delete", "number": n})
}

// Toggle enables or disables the firewall; the agent rejects it when the
// node's allow_toggle is off.
func (a *FirewallAPI) Toggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	json.NewDecoder(r.Body).Decode(&body) // optional body; empty = disable
	uuid := chi.URLParam(r, "uuid")
	if err := proxyRequest(a.manager, w, uuid, "ufw.toggle", map[string]bool{"enabled": body.Enabled}); err != nil {
		return
	}
	a.hooks.Fire(webhooks.EventFirewallChange, map[string]any{"node_id": uuid, "action": "toggle", "enabled": body.Enabled})
}
