package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"gopit/internal/protocol"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/webhooks"
)

// DockerAPI proxies docker.* requests from the browser to the node's agent.
type DockerAPI struct {
	manager  *nodemanager.Manager
	upgrader websocket.Upgrader
	hooks    *webhooks.Store
}

// NewDockerAPI wires the docker routes.
func NewDockerAPI(m *nodemanager.Manager, hooks *webhooks.Store) *DockerAPI {
	return &DockerAPI{
		manager: m,
		hooks:   hooks,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true }, // same-origin JWT cookie already checked
		},
	}
}

const (
	dockerRequestTimeout = 30 * time.Second
	maxComposeYaml       = 1 << 20 // 1MB
)

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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
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

// ComposeValidate runs `docker compose config` on the agent for a candidate
// YAML and returns the parsed services (or the config error), before deploy.
func (a *DockerAPI) ComposeValidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Yaml string `json:"yaml"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxComposeYaml)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Yaml == "" {
		writeErr(w, http.StatusBadRequest, "name and yaml required")
		return
	}
	if len(body.Yaml) > maxComposeYaml {
		writeErr(w, http.StatusBadRequest, "compose yaml exceeds 1MB limit")
		return
	}
	a.proxy(w, chi.URLParam(r, "uuid"), "docker.compose.validate", map[string]string{"name": body.Name, "yaml": body.Yaml})
}

func (a *DockerAPI) ComposeDeploy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Yaml string `json:"yaml"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxComposeYaml)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.Yaml == "" {
		writeErr(w, http.StatusBadRequest, "name and yaml required")
		return
	}
	if len(body.Yaml) > maxComposeYaml {
		writeErr(w, http.StatusBadRequest, "compose yaml exceeds 1MB limit")
		return
	}
	// Dry-run: validate compose config before deploy. -f must precede the
	// config subcommand: docker compose only accepts global flags there.
	if _, err := runCompose(10*time.Second, body.Yaml, "-p", body.Name, "-f", "-", "config", "--quiet"); err != nil {
		writeErr(w, http.StatusBadRequest, "compose config validation failed: "+err.Error())
		return
	}
	uuid := chi.URLParam(r, "uuid")
	if err := proxyRequest(a.manager, w, uuid, "docker.compose.deploy", map[string]string{"name": body.Name, "yaml": body.Yaml}); err != nil {
		return
	}
	a.hooks.Fire(webhooks.EventDockerDeploy, map[string]string{"node_id": uuid, "name": body.Name})
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
	defer TrackWS(ws)()
	ws.SetReadLimit(4 << 20) // bounded browser frames, matches the agent-side limit
	// Subscribe BEFORE the handshake request: log events emitted between the
	// agent's first lines and our subscription would otherwise race-drop.
	sub := make(chan protocol.Envelope, 512)
	conn.Subscribe(sub)
	defer conn.Unsubscribe(sub)
	resp, err := conn.Request("docker.container.logs", map[string]string{"container_id": cid}, 15*time.Second)
	if err != nil || (resp.Error != nil && *resp.Error != "") {
		ws.WriteJSON(protocol.NewErrorResponse("", remoteMsg(err, resp)))
		ws.Close()
		return
	}
	ws.WriteJSON(resp) // {stream:true} handshake

	ping := make(chan struct{})
	var closePing sync.Once
	// Keepalive: the browser sends no frames on this socket, so a plain read
	// deadline would kill quiet streams. The server pings (browsers auto-pong)
	// and the pong handler rolls a 90s read deadline, so a half-open conn dies
	// within ~90s while a silent stream stays up.
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					closePing.Do(func() { close(ping) })
					return
				}
			case <-ping:
				return
			}
		}
	}()
	go func() {
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				closePing.Do(func() { close(ping) })
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
			if cid != "" { // the agent emits a stream per container; keep only ours
				var p struct {
					ContainerID string `json:"container_id"`
				}
				json.Unmarshal(e.Payload, &p)
				if p.ContainerID != cid {
					continue
				}
			}
			if err := ws.WriteJSON(e); err != nil {
				ws.Close()
				return
			}
		case <-ping: // browser closed -> tell the agent to stop following
			ws.Close()
			conn.WriteJSON(protocol.NewRequest("docker.container.logs.stop", map[string]string{"container_id": cid}))
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

func runCompose(timeout time.Duration, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", nil // docker not available on server; skip dry-run
		}
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "'compose' is not a docker command") {
			return "", nil // compose plugin not available; skip dry-run
		}
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %s", err, msg)
	}
	return string(out), nil
}
