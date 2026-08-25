// Package agent orchestrates the gopitd agent: config, beacon, WS server.
package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopit/internal/agent/config"
	"gopit/internal/agent/discovery"
	dockerd "gopit/internal/agent/docker"
	"gopit/internal/agent/system"
	"gopit/internal/agent/ufw"
	agentws "gopit/internal/agent/ws"
	"gopit/internal/protocol"

	"github.com/google/uuid"
)

// fwBackend is the firewall implementation behind the ufw.* WS methods.
type fwBackend interface {
	Call(method string, payload json.RawMessage) (any, error)
}

// Agent ties config, beacon and WS server together.
type Agent struct {
	cfg       *config.Config
	uuid      string
	beacon    *discovery.Beacon
	ws        *agentws.Server
	collector *system.Collector
	docker    *dockerd.API
}

// New loads config from path and prepares the agent.
func New(cfgPath string) (*Agent, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	id, err := loadOrCreateUUID(cfg.UUIDPath)
	if err != nil {
		return nil, err
	}
	col := system.NewCollector()
	info := col.Info(cfg.Port)
	info.UUID = id
	info.TLS = cfg.TLSCert != ""

	beacon := discovery.NewBeacon(func() protocol.NodeInfo {
		// re-resolve per reply so a DHCP change is announced without restart
		i := col.Info(cfg.Port)
		i.UUID, i.TLS = id, cfg.TLSCert != ""
		return i
	})
	dk, err := dockerd.New()
	if err != nil {
		slog.Warn("docker unavailable, docker.* methods will error", "err", err)
		dk = nil // handler answers with a proper error envelope
	}
	h := &agentws.Handler{
		OnStats: col.Stats,
		OnInfo:  func() protocol.NodeInfo { return info },
		Docker:  dk,
		FW:      newFirewall(cfg),
		Term: agentws.TermConf{
			Record:       cfg.Terminal.Record,
			RecordingDir: cfg.Terminal.RecordingDir,
			NodeID:       id,
		},
	}
	wsSrv := agentws.NewServer(cfg.Token, time.Duration(cfg.StatsIntervalSecs)*time.Second, h)
	wsSrv.SetTLS(cfg.TLSCert, cfg.TLSKey)
	return &Agent{cfg: cfg, uuid: id, beacon: beacon, ws: wsSrv, collector: col}, nil
}

// newFirewall picks the firewall backend from config. The nftfw (direct
// netlink) backend is Linux-only and lives in firewall_linux.go; other
// platforms get a backend that reports the feature as unavailable.
func newFirewall(cfg *config.Config) fwBackend {
	if cfg.Firewall == "ufw" {
		slog.Info("firewall backend: ufw (sudo)")
		return ufw.New(cfg.Ufw.BinaryPath, cfg.Ufw.AllowToggle)
	}
	slog.Info("firewall backend: nftfw (no sudo)")
	return nftfwNew(cfg)
}

// Run starts the beacon and WS server, blocking until either fails.
func (a *Agent) Run() error {
	addr := a.cfg.ListenAddr + ":" + itoa(a.cfg.Port)
	slog.Info("starting agent", "uuid", a.uuid, "addr", addr)
	if err := a.beacon.Start(a.cfg.Port); err != nil {
		return err
	}
	defer a.beacon.Close()
	err := a.ws.Start(addr)
	return err
}

// Close stops everything.
func (a *Agent) Close() {
	a.ws.Close()
	a.beacon.Close()
}

// loadOrCreateUUID reads the persisted node UUID or generates and writes one.
func loadOrCreateUUID(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(b)) // tolerate a stray trailing newline: identity must survive
		if _, err := uuid.Parse(id); err == nil {
			return id, nil
		}
	}
	id := uuid.NewString()
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
