package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gopit/internal/server/discovery"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
)

// NodesAPI serves node management routes.
type NodesAPI struct {
	store     *store.Store
	manager   *nodemanager.Manager
	disc      discovery.Client
	pairToken string // server pairing_token: assigned to token-less nodes at approval
}

// NewNodesAPI wires the node routes.
func NewNodesAPI(s *store.Store, m *nodemanager.Manager, disc discovery.Client, pairToken string) *NodesAPI {
	return &NodesAPI{store: s, manager: m, disc: disc, pairToken: pairToken}
}

// List returns all known nodes.
func (a *NodesAPI) List(w http.ResponseWriter, r *http.Request) {
	nodes, err := a.store.ListNodes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	if nodes == nil {
		nodes = []store.Node{}
	}
	writeJSON(w, http.StatusOK, nodes)
}

// Discover broadcasts and upserts replies as pending nodes.
func (a *NodesAPI) Discover(w http.ResponseWriter, r *http.Request) {
	replies, err := a.disc.Discover()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "discovery failed: "+err.Error())
		return
	}
	nodes := discovery.UpsertReplies(a.store, replies)
	if nodes == nil {
		nodes = []store.Node{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "found": len(nodes)})
}

type manualNode struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Hostname string `json:"hostname"`
	Token    string `json:"token"`
}

// Create adds a node manually as pending.
func (a *NodesAPI) Create(w http.ResponseWriter, r *http.Request) {
	var m manualNode
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.IP == "" || m.Port == 0 || m.Token == "" {
		writeErr(w, http.StatusBadRequest, "ip, port and token required")
		return
	}
	if m.Port < 1 || m.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "invalid port")
		return
	}
	if err := validateIP(m.IP); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid ip: "+err.Error())
		return
	}
	if m.Hostname != "" {
		if err := validateHostname(m.Hostname); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid hostname: "+err.Error())
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	n := &store.Node{
		ID: uuid.NewString(), Hostname: m.Hostname, IP: m.IP, Port: m.Port,
		Status: store.StatusPending, FirstSeen: now, LastSeen: now, Token: m.Token,
	}
	if err := a.store.UpsertNode(n); err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func validateIP(ip string) error {
	if net.ParseIP(ip) == nil {
		return errors.New("not a valid IPv4 or IPv6 address")
	}
	return nil
}

func validateHostname(hostname string) error {
	if len(hostname) > 253 {
		return errors.New("hostname too long (max 253 chars)")
	}
	// RFC 1123: labels 1-63 chars, alphanumeric + hyphen, no leading/trailing hyphen
	labels := strings.Split(hostname, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return errors.New("invalid label length")
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("label cannot start or end with hyphen")
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return errors.New("label contains invalid character")
			}
		}
	}
	return nil
}

// Approve marks a node approved and connects to it. A discovered node with no
// stored token is rejected: the operator must supply the agent token first.
func (a *NodesAPI) Approve(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "uuid")
	n, err := a.store.GetNode(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	if n.Token == "" {
		if a.pairToken != "" {
			// agents installed with the server's pairing token (install.sh
			// TOKEN=...) carry it; assign it so approval just works.
			if err := a.store.SetNodeToken(id, a.pairToken); err != nil {
				writeErr(w, http.StatusInternalServerError, "db failed")
				return
			}
			n.Token = a.pairToken
		} else {
			writeErr(w, http.StatusBadRequest, "node needs an agent token before approval (set it via token endpoint)")
			return
		}
	}
	if err := a.store.SetNodeStatus(id, store.StatusApproved); err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	n.Status = store.StatusApproved // reflect the write in the response
	go a.manager.Reconcile()
	writeJSON(w, http.StatusOK, n)
}

type nodeToken struct {
	Token string `json:"token"`
}

// SetToken stores the agent token for a node. This is the operator's way of
// pairing a discovered node; the token is never sent over UDP.
func (a *NodesAPI) SetToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "uuid")
	var t nodeToken
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.Token == "" {
		writeErr(w, http.StatusBadRequest, "token required")
		return
	}
	if _, err := a.store.GetNode(id); err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	if err := a.store.SetNodeToken(id, t.Token); err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// WizardInfo returns first-run onboarding state: whether the wizard is done,
// the fleet pairing token for agents, the node install one-liner, and whether
// this host has a non-loopback interface for UDP broadcast discovery.
func (a *NodesAPI) WizardInfo(w http.ResponseWriter, r *http.Request) {
	done, _ := a.store.GetSetting("first_run_done")
	writeJSON(w, http.StatusOK, map[string]any{
		"done":        done == "1",
		"token":       a.pairToken,
		"install_cmd": "sudo TOKEN=" + a.pairToken + " ./install.sh agent",
		"broadcast":   broadcastCapable(),
	})
}

// WizardDone marks the first-run wizard complete (skip).
func (a *NodesAPI) WizardDone(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetSetting("first_run_done", "1"); err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// broadcastCapable reports whether any non-loopback interface has an IPv4
// address, i.e. whether UDP broadcast discovery can leave this host.
func broadcastCapable() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				return true
			}
		}
	}
	return false
}

// Delete removes a node.
func (a *NodesAPI) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "uuid")
	if err := a.store.DeleteNode(id); err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	a.manager.Drop(id) // don't leave the live conn relaying events for a deleted node
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
