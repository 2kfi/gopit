package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"gopit/internal/protocol"
	"gopit/internal/server/nodemanager"
)

// DockerAPI proxies docker.* requests from the browser to the node's agent.
type DockerAPI struct {
	manager  *nodemanager.Manager
	upgrader websocket.Upgrader
}

// NewDockerAPI wires the docker routes.
func NewDockerAPI(m *nodemanager.Manager) *DockerAPI {
	return &DockerAPI{
		manager: m,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true }, // same-origin JWT cookie already checked
		},
	}
}

const dockerRequestTimeout = 30 * time.Second

// proxy runs one request against the node's agent and forwards the payload.
// Agent offline -> 503; agent error/timeout -> 502 with its message.
func (a *DockerAPI) proxy(w http.ResponseWriter, nodeID, method string, payload any) {
	proxyRequest(a.manager, w, nodeID, method, payload)
}

func (a *DockerAPI) Containers(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.containers.list", nil)
}

func (a *DockerAPI) Inspect(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.container.inspect", map[string]string{"container_id": chi.URLParam(r, "id")})
}

func (a *DockerAPI) Start(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.container.start", map[string]string{"container_id": chi.URLParam(r, "id")})
}

func (a *DockerAPI) Stop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Timeout *int `json:"timeout"`
	}
	json.NewDecoder(r.Body).Decode(&body) // optional body; empty is fine
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.container.stop", map[string]any{
		"container_id": chi.URLParam(r, "id"),
		"timeout":      body.Timeout,
	})
}

func (a *DockerAPI) Remove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Force   bool `json:"force"`
		Volumes bool `json:"volumes"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.container.remove", map[string]any{
		"container_id": chi.URLParam(r, "id"),
		"force":        body.Force,
		"volumes":      body.Volumes,
	})
}

func (a *DockerAPI) Images(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.images.list", nil)
}

func (a *DockerAPI) RemoveImage(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.image.remove", map[string]string{"image_id": chi.URLParam(r, "id")})
}

func (a *DockerAPI) Volumes(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.volumes.list", nil)
}

func (a *DockerAPI) RemoveVolume(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.volume.remove", map[string]string{"name": chi.URLParam(r, "id")})
}

func (a *DockerAPI) ComposeStacks(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.compose.list", nil)
}

func (a *DockerAPI) ComposeDeploy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Yaml string `json:"yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Yaml == "" {
		writeErr(w, http.StatusBadRequest, "name and yaml required")
		return
	}
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.compose.deploy", map[string]string{"name": body.Name, "yaml": body.Yaml})
}

func (a *DockerAPI) ComposeDown(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.compose.down", map[string]string{"name": chi.URLParam(r, "name")})
}

func (a *DockerAPI) ComposePS(w http.ResponseWriter, r *http.Request) {
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.compose.ps", map[string]string{"name": chi.URLParam(r, "name")})
}

// Logs upgrades to WS and relays the agent's docker.container.log stream to
// the browser. Closing the browser WS cancels the agent-side stream.
func (a *DockerAPI) Logs(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "uuid")
	cid := chi.URLParam(r, "id")
	conn := a.manager.Conn(nodeID)
	if conn == nil {
		writeErr(w, http.StatusServiceUnavailable, "node offline")
		return
	}
	ws, err := a.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	resp, err := conn.Request("docker.container.logs", map[string]string{"container_id": cid}, 15*time.Second)
	if err != nil || (resp.Error != nil && *resp.Error != "") {
		ws.WriteJSON(protocol.NewErrorResponse("", remoteMsg(err, resp)))
		ws.Close()
		return
	}
	ws.WriteJSON(resp) // {stream:true} handshake
	sub := make(chan protocol.Envelope, 512)
	conn.Subscribe(sub)
	defer conn.Unsubscribe(sub)

	ping := make(chan struct{})
	go func() {
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				close(ping)
				return
			}
		}
	}()
	for {
		select {
		case e, ok := <-sub:
			if !ok {
				ws.Close()
				return
			}
			if !strings.HasPrefix(e.Method, "docker.container.log") {
				continue
			}
			if err := ws.WriteJSON(e); err != nil {
				ws.Close()
				return
			}
		case <-ping: // browser closed -> tell the agent to stop following
			ws.Close()
			conn.WS.WriteJSON(protocol.NewRequest("docker.container.logs.stop", map[string]string{"container_id": cid}))
			return
		}
	}
}

func remoteMsg(err error, resp protocol.Envelope) string {
	if err != nil {
		return err.Error()
	}
	return *resp.Error
}
