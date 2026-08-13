package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gopit/internal/server/discovery"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
)

// NodesAPI serves node management routes.
type NodesAPI struct {
	store   *store.Store
	manager *nodemanager.Manager
	disc    discovery.Client
}

// NewNodesAPI wires the node routes.
func NewNodesAPI(s *store.Store, m *nodemanager.Manager, disc discovery.Client) *NodesAPI {
	return &NodesAPI{store: s, manager: m, disc: disc}
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
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.IP == "" || m.Port == 0 || m.Token == "" {
		writeErr(w, http.StatusBadRequest, "ip, port and token required")
		return
	}
	if m.Port < 1 || m.Port > 65535 {
		writeErr(w, http.StatusBadRequest, "invalid port")
		return
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
		writeErr(w, http.StatusBadRequest, "node needs an agent token before approval (set it via token endpoint)")
		return
	}
	if err := a.store.SetNodeStatus(id, store.StatusApproved); err != nil {
		writeErr(w, http.StatusInternalServerError, "db failed")
		return
	}
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

// Delete removes a node.
func (a *NodesAPI) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "uuid")
	if err := a.store.DeleteNode(id); err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
