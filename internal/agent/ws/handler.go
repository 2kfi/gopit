package ws

import (
	"encoding/json"
	"log/slog"
	"strings"

	"gopit/internal/agent/docker"
	"gopit/internal/agent/wsconn"
	"gopit/internal/protocol"
)

// Handler dispatches authenticated requests to method handlers.
type Handler struct {
	OnStats func() protocol.SystemStats
	OnInfo  func() protocol.NodeInfo
	Docker  *docker.API // nil when docker is unavailable
	FW      fwAPI       // never nil; both backends degrade to clean errors
	Term    TermConf    // optional session recording
}

// TermConf configures PTY session recording for this agent.
type TermConf struct {
	Record       bool   // write ttyrec files for every terminal session
	RecordingDir string // base dir; sessions land in <dir>/<node_id>/<ts>.ttyrec
	NodeID       string // this agent's node UUID (recording subdir)
}

// fwAPI is the firewall backend behind the ufw.* methods (nftfw or ufw).
type fwAPI interface {
	Call(method string, payload json.RawMessage) (any, error)
}

// Handle processes one request envelope, writing the response with the same id.
// done closes when the client disconnects; streaming handlers use it to stop.
func (h *Handler) Handle(c *wsconn.Conn, e protocol.Envelope, done <-chan struct{}, st *ConnState) {
	switch e.Method {
	case protocol.MethodTerminalOpen:
		h.TerminalOpen(c, e, st)
		return
	case protocol.MethodTerminalResize:
		h.TerminalResize(c, e, st)
		return
	case protocol.MethodTerminalClose:
		h.TerminalClose(c, e, st)
		return
	}
	if h.FW != nil {
		switch e.Method {
		case MethodUfwStatus, MethodUfwRuleAdd, MethodUfwRuleDel, MethodUfwToggle, MethodUfwPreview:
			payload, err := h.FW.Call(e.Method, e.Payload)
			if err != nil {
				respondErr(c, e.ID, err.Error())
				return
			}
			respond(c, e.ID, payload)
			return
		}
	}
	if h.Docker != nil {
		switch e.Method {
		case MethodDockerContainerLogs: // streaming: response + events until cancelled
			h.Docker.StreamLogs(c, e, done)
			return
		case MethodDockerContainerLogsStop:
			h.Docker.StopLogs(e.Payload)
			respond(c, e.ID, map[string]string{"status": "ok"})
			return
		}
		var (
			payload any
			err     error
		)
		if strings.HasPrefix(e.Method, "docker.compose.") {
			payload, err = h.Docker.ComposeCall(e.Method, e.Payload)
		} else if strings.HasPrefix(e.Method, "docker.") {
			payload, err = h.Docker.Call(e.Method, e.Payload)
		}
		if err != nil {
			respondErr(c, e.ID, err.Error())
			return
		}
		if payload != nil {
			respond(c, e.ID, payload)
			return
		}
	}
	switch e.Method {
	case MethodSystemInfo:
		respond(c, e.ID, h.OnInfo())
	case MethodSystemStats:
		respond(c, e.ID, h.OnStats())
	default:
		respondErr(c, e.ID, "unknown method: "+e.Method)
	}
}

func respond(c *wsconn.Conn, id string, payload any) {
	if err := c.WriteJSON(protocol.NewResponse(id, payload)); err != nil {
		slog.Warn("agent ws write failed", "err", err)
	}
}

func respondErr(c *wsconn.Conn, id, msg string) {
	if err := c.WriteJSON(protocol.NewErrorResponse(id, msg)); err != nil {
		slog.Warn("agent ws write failed", "err", err)
	}
}
