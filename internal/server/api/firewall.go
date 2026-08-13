package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gopit/internal/server/nodemanager"
)

// FirewallAPI proxies ufw.* requests from the browser to the node's agent.
type FirewallAPI struct {
	manager *nodemanager.Manager
}

// NewFirewallAPI wires the firewall routes.
func NewFirewallAPI(m *nodemanager.Manager) *FirewallAPI {
	return &FirewallAPI{manager: m}
}

// proxyRequest runs one request against the node's agent and forwards the
// payload. Agent offline -> 503; agent error/timeout -> 502 with its message.
func proxyRequest(m *nodemanager.Manager, w http.ResponseWriter, nodeID, method string, payload any) {
	conn := m.Conn(nodeID)
	if conn == nil {
		writeErr(w, http.StatusServiceUnavailable, "node offline")
		return
	}
	resp, err := conn.Request(method, payload, dockerRequestTimeout)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if resp.Error != nil && *resp.Error != "" {
		writeErr(w, http.StatusBadGateway, *resp.Error)
		return
	}
	writeJSON(w, http.StatusOK, resp.Payload)
}

// Status returns the live ufw status, default policies and rule table.
func (a *FirewallAPI) Status(w http.ResponseWriter, r *http.Request) {
	proxyRequest(a.manager, w, chi.URLParam(r, "uuid"), "ufw.status", nil)
}

// AddRule adds one allow/deny/reject rule for port and protocol.
func (a *FirewallAPI) AddRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Protocol  string `json:"protocol"`
		Port      int    `json:"port"`
		Action    string `json:"action"`
		From      string `json:"from"`
		Interface string `json:"interface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}
	switch body.Protocol {
	case "tcp", "udp", "both":
	default:
		writeErr(w, http.StatusBadRequest, "protocol must be tcp, udp or both")
		return
	}
	switch body.Action {
	case "allow", "deny", "reject":
	default:
		writeErr(w, http.StatusBadRequest, "action must be allow, deny or reject")
		return
	}
	proxyRequest(a.manager, w, chi.URLParam(r, "uuid"), "ufw.rule.add", body)
}

// DeleteRule deletes a rule by its ufw numbering.
func (a *FirewallAPI) DeleteRule(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil || n < 1 {
		writeErr(w, http.StatusBadRequest, "invalid rule number")
		return
	}
	proxyRequest(a.manager, w, chi.URLParam(r, "uuid"), "ufw.rule.delete", map[string]int{"number": n})
}

// Toggle enables or disables the firewall; the agent rejects it when the
// node's allow_toggle is off.
func (a *FirewallAPI) Toggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	json.NewDecoder(r.Body).Decode(&body) // optional body; empty = disable
	proxyRequest(a.manager, w, chi.URLParam(r, "uuid"), "ufw.toggle", map[string]bool{"enabled": body.Enabled})
}
