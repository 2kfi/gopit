package ws

import (
	"log/slog"
	"strings"

	"gopit/internal/agent/docker"
	"gopit/internal/agent/ufw"
	"gopit/internal/agent/wsconn"
	"gopit/internal/protocol"
)

// Handler dispatches authenticated requests to method handlers.
type Handler struct {
	OnStats func() protocol.SystemStats
	OnInfo  func() protocol.NodeInfo
	Docker  *docker.API // nil when docker is unavailable
	Ufw     *ufw.API    // never nil; degrades to "ufw not available" errors
}

// Handle processes one request envelope, writing the response with the same id.
// done closes when the client disconnects; streaming handlers use it to stop.
func (h *Handler) Handle(c *wsconn.Conn, e protocol.Envelope, done <-chan struct{}, st *ConnState) {
	switch e.Method {
	case MethodTerminalOpen:
		h.TerminalOpen(c, e, st)
		return
	case MethodTerminalResize:
		h.TerminalResize(c, e, st)
		return
	case MethodTerminalClose:
		h.TerminalClose(c, e, st)
		return
	}
	if h.Ufw != nil {
		switch e.Method {
		case MethodUfwStatus, MethodUfwRuleAdd, MethodUfwRuleDel, MethodUfwToggle:
			payload, err := h.Ufw.Call(e.Method, e.Payload)
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
