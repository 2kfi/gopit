// Package docker wraps the Docker Engine API for the agent's docker.* WS methods.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"gopit/internal/agent/wsconn"
	"gopit/internal/protocol"
)

// API serves the docker.* WS methods.
type API struct {
	cli     *client.Client
	mu      sync.Mutex
	streams map[string]*logStream // containerID -> stream, one follow per container
}

type logStream struct{ cancel context.CancelFunc }

// New detects the docker socket (rootless first, then /var/run) and builds a client.
func New() (*API, error) {
	sock := "/var/run/docker.sock"
	if xd := os.Getenv("XDG_RUNTIME_DIR"); xd != "" {
		if p := filepath.Join(xd, "docker.sock"); pathExists(p) {
			sock = p
		}
	}
	cli, err := client.NewClientWithOpts(
		client.WithHost("unix://"+sock),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &API{cli: cli, streams: map[string]*logStream{}}, nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// callTimeout bounds one-shot SDK calls so a hung daemon never hangs the agent.
var callTimeout = 30 * time.Second

// Request payloads (keys are the wire contract with the server).
type ContainerReq struct {
	ContainerID string `json:"container_id"`
}

type StopReq struct {
	ContainerID string `json:"container_id"`
	Timeout     *int   `json:"timeout,omitempty"`
}

type RemoveReq struct {
	ContainerID string `json:"container_id"`
	Force       bool   `json:"force"`
	Volumes     bool   `json:"volumes"`
}

type ImageReq struct {
	ImageID string `json:"image_id"`
	Force   bool   `json:"force"`
}

type VolumeReq struct {
	Name  string `json:"name"`
	Force bool   `json:"force"`
}

type LogsReq struct {
	ContainerID string `json:"container_id"`
}

// Response payloads.
type ContainerSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Ports   []Port `json:"ports"`
	Created int64  `json:"created"` // unix seconds
}

type Port struct {
	IP          string `json:"ip,omitempty"`
	PublicPort  uint16 `json:"public_port,omitempty"`
	PrivatePort uint16 `json:"private_port"`
	Type        string `json:"type"`
}

type ImageSummary struct {
	ID         string   `json:"id"`
	RepoTags   []string `json:"repo_tags"`
	Size       int64    `json:"size"`
	Created    int64    `json:"created"`
	Containers int64    `json:"containers"`
}

type VolumeSummary struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
	CreatedAt  string `json:"created_at"`
}

// Call executes a one-shot docker.* method with the raw request payload and
// returns the response payload (already JSON-marshalable).
func (a *API) Call(method string, payload json.RawMessage) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	switch method {
	case "docker.containers.list":
		return a.listContainers(ctx)
	case "docker.container.inspect":
		var req ContainerReq
		if err := json.Unmarshal(payload, &req); err != nil || req.ContainerID == "" {
			return nil, errors.New("container_id required")
		}
		return a.cli.ContainerInspect(ctx, req.ContainerID)
	case "docker.container.start":
		var req ContainerReq
		if err := json.Unmarshal(payload, &req); err != nil || req.ContainerID == "" {
			return nil, errors.New("container_id required")
		}
		if err := a.cli.ContainerStart(ctx, req.ContainerID, container.StartOptions{}); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	case "docker.container.stop":
		var req StopReq
		if err := json.Unmarshal(payload, &req); err != nil || req.ContainerID == "" {
			return nil, errors.New("container_id required")
		}
		if err := a.cli.ContainerStop(ctx, req.ContainerID, container.StopOptions{Timeout: req.Timeout}); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	case "docker.container.remove":
		var req RemoveReq
		if err := json.Unmarshal(payload, &req); err != nil || req.ContainerID == "" {
			return nil, errors.New("container_id required")
		}
		if err := a.cli.ContainerRemove(ctx, req.ContainerID, container.RemoveOptions{Force: req.Force, RemoveVolumes: req.Volumes}); err != nil {
			return nil, err
		}
		return map[string]string{"status": "ok"}, nil
	case "docker.images.list":
		imgs, err := a.cli.ImageList(ctx, image.ListOptions{All: true})
		if err != nil {
			return nil, err
		}
		out := make([]ImageSummary, 0, len(imgs))
		for _, im := range imgs {
			out = append(out, ImageSummary{ID: im.ID, RepoTags: im.RepoTags, Size: im.Size, Created: im.Created, Containers: im.Containers})
		}
		return out, nil
	case "docker.image.remove":
		var req ImageReq
		if err := json.Unmarshal(payload, &req); err != nil || req.ImageID == "" {
			return nil, errors.New("image_id required")
		}
		return a.cli.ImageRemove(ctx, req.ImageID, image.RemoveOptions{Force: req.Force, PruneChildren: true})
	case "docker.volumes.list":
		vols, err := a.cli.VolumeList(ctx, volume.ListOptions{})
		if err != nil {
			return nil, err
		}
		out := make([]VolumeSummary, 0, len(vols.Volumes))
		for _, v := range vols.Volumes {
			out = append(out, VolumeSummary{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt})
		}
		return out, nil
	case "docker.volume.remove":
		var req VolumeReq
		if err := json.Unmarshal(payload, &req); err != nil || req.Name == "" {
			return nil, errors.New("name required")
		}
		return map[string]string{"status": "ok"}, a.cli.VolumeRemove(ctx, req.Name, req.Force)
	default:
		return nil, fmt.Errorf("unknown docker method: %s", method)
	}
}

func (a *API) listContainers(ctx context.Context) (any, error) {
	list, err := a.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]ContainerSummary, 0, len(list))
	for _, c := range list {
		out = append(out, ContainerSummary{
			ID:      c.ID,
			Name:    strings.TrimPrefix(c.Names[0], "/"),
			Image:   c.Image,
			State:   c.State,
			Status:  c.Status,
			Ports:   toPorts(c.Ports),
			Created: c.Created,
		})
	}
	return out, nil
}

func toPorts(ps []container.Port) []Port {
	out := make([]Port, 0, len(ps))
	for _, p := range ps {
		out = append(out, Port{IP: p.IP, PublicPort: p.PublicPort, PrivatePort: p.PrivatePort, Type: p.Type})
	}
	return out
}

// StreamLogs follows a container's logs: responds {stream:true}, then emits
// docker.container.log events ({container_id, line}) until the stream ends or
// is cancelled (client disconnect via done, or docker.container.logs.stop).
func (a *API) StreamLogs(c *wsconn.Conn, e protocol.Envelope, done <-chan struct{}) {
	var req LogsReq
	if err := json.Unmarshal(e.Payload, &req); err != nil || req.ContainerID == "" {
		writeEnv(c, protocol.NewErrorResponse(e.ID, "container_id required"))
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ls := &logStream{cancel: cancel}
	a.mu.Lock()
	if old, ok := a.streams[req.ContainerID]; ok { // one follow per container; restart wins
		old.cancel()
	}
	a.streams[req.ContainerID] = ls
	a.mu.Unlock()

	finished := make(chan struct{})
	go func() { // unblock the SDK reader when the client goes away
		select {
		case <-done:
			cancel()
		case <-finished:
		}
	}()
	go func() {
		errMsg := ""
		defer func() {
			close(finished)
			a.mu.Lock()
			if a.streams[req.ContainerID] == ls { // only ours: a restart may have replaced it
				delete(a.streams, req.ContainerID)
			}
			a.mu.Unlock()
		}()
		defer func() {
			p := map[string]string{"container_id": req.ContainerID}
			if errMsg != "" {
				p["error"] = errMsg
			}
			writeEnv(c, protocol.NewEvent(MethodLogsEnd, p))
		}()
		rc, err := a.cli.ContainerLogs(ctx, req.ContainerID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true})
		if err != nil {
			errMsg = err.Error()
			return
		}
		defer rc.Close()
		emit := func(line string) {
			writeEnv(c, protocol.NewEvent(MethodLogLine, map[string]string{"container_id": req.ContainerID, "line": line}))
		}
		// stdout/stderr are multiplexed in one stream; stdcopy demuxes them.
		// ponytail: two line-splitters instead of a demux-to-buffer pipeline
		out, errOut := newLineWriter(emit), newLineWriter(emit)
		stdcopy.StdCopy(out, errOut, rc)
		out.Flush()
		errOut.Flush()
	}()
	writeEnv(c, protocol.NewResponse(e.ID, map[string]bool{"stream": true}))
}

// Method names for log events (agent -> server -> browser).
const (
	MethodLogLine = "docker.container.log"
	MethodLogsEnd = "docker.container.logs.end"
)

// StopLogs cancels a running log stream (fire-and-forget from the server when
// the browser disconnects).
func (a *API) StopLogs(payload json.RawMessage) {
	var req LogsReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return
	}
	a.mu.Lock()
	if ls, ok := a.streams[req.ContainerID]; ok {
		ls.cancel()
	}
	a.mu.Unlock()
}

// lineWriter buffers partial lines and emits complete ones.
type lineWriter struct {
	buf  []byte
	emit func(string)
}

func newLineWriter(emit func(string)) *lineWriter { return &lineWriter{emit: emit} }

func (w *lineWriter) Write(p []byte) (int, error) {
	for {
		i := indexByte(p, '\n')
		if i < 0 {
			w.buf = append(w.buf, p...)
			return len(p), nil
		}
		w.buf = append(w.buf, p[:i]...)
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
		p = p[i+1:]
	}
}

// Flush emits any trailing partial line; called by clients when the stream closes.
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// writeEnv sends an envelope, logging (not panicking) on a dead connection.
func writeEnv(c *wsconn.Conn, e protocol.Envelope) {
	if err := c.WriteJSON(e); err != nil {
		slog.Warn("docker ws write failed", "err", err)
	}
}
