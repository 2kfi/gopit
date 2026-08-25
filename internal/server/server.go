// Package server orchestrates the gopit server: config, store, discovery,
// node manager and HTTP API.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gopit/internal/protocol"
	"gopit/internal/server/api"
	"gopit/internal/server/config"
	"gopit/internal/server/discovery"
	"gopit/internal/server/metrics"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
	"gopit/internal/server/webhooks"

	"github.com/google/uuid"
)

// Server is the gopit control plane.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	manager *nodemanager.Manager
	http    *http.Server
}

// New loads config, opens the store, bootstraps admin + JWT secret,
// and starts the node manager in the background.
func New(cfgPath string) (*Server, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if err := cfg.ResolveSecrets(); err != nil {
		return nil, err
	}
	if cfg.TLSSkipVerify {
		slog.Warn("TLS verification is DISABLED (tls_skip_verify=true). This is insecure and should only be used for testing. Set tls_skip_verify=false and provide valid certificates for production.")
	}
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := bootstrap(s, cfg); err != nil {
		s.Close()
		return nil, err
	}
	hooksCfg := make([]webhooks.Hook, 0, len(cfg.Webhooks))
	for _, h := range cfg.Webhooks {
		hooksCfg = append(hooksCfg, webhooks.Hook{URL: h.URL, Secret: h.Secret, Events: h.Events})
	}
	hooks := webhooks.New(hooksCfg)
	m := nodemanager.New(s, nodemanager.Callbacks{
		OnStatus: func(nodeID, status string) {
			if status == store.StatusOnline {
				// first-run wizard completes automatically once a node is online
				if done, _ := s.GetSetting("first_run_done"); done != "1" {
					s.SetSetting("first_run_done", "1")
				}
			}
			s.SetNodeStatus(nodeID, status)
			if status == store.StatusOnline {
				hooks.Fire(webhooks.EventNodeUp, map[string]string{"node_id": nodeID, "status": status})
			} else {
				hooks.Fire(webhooks.EventNodeDown, map[string]string{"node_id": nodeID, "status": status})
			}
		},
		OnStats: func(nodeID string, stats protocol.SystemStats) {}, // live relay goes via WS subscribers
	}, cfg.TLSSkipVerify)
	srv := &Server{cfg: cfg, store: s, manager: m}

	disc := &discovery.Client{
		BroadcastAddr: cfg.DiscoveryBroadcast,
		Port:          cfg.DiscoveryPort,
		Timeout:       500 * time.Millisecond,
	}
	handler := api.Router(s, m, disc, cfg.JWTSecret, cfg.PairingToken, cfg.TLSSkipVerify, cfg.TrustProxy, cfg.RateLimitPerMin, cfg.PasswordMinScore, hooks)
	srv.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		// ReadTimeout/WriteTimeout stay zero: this server upgrades
		// WebSockets (gorilla hijack) and long-lived terminal streams
		// must not be killed by HTTP timeouts.
	}
	go m.Reconcile()
	go periodicReconcile(m)
	srv.store.StartMaintenance(cfg.DBMaintenanceHours)
	go metricsLoop(s, cfg, m)
	return srv, nil
}

// Run blocks serving HTTP.
func (s *Server) Run() error {
	slog.Info("server listening", "addr", s.cfg.ListenAddr)
	return s.http.ListenAndServe()
}

// Close shuts everything down.
func (s *Server) Close() error {
	s.manager.Close()
	return s.store.Close()
}

// Shutdown gracefully drains in-flight HTTP handlers within ctx, then closes
// the manager and the store. Hijacked WS conns are not drained by
// http.Shutdown: browser streams get a close frame first (DrainWS bounds the
// wait by the same ctx), the manager close cuts the agent side, and the WS
// read deadlines cut any browser that ignores the close frame.
func (s *Server) Shutdown(ctx context.Context) error {
	// http.Shutdown closes the listener immediately (no new WS upgrades)
	// and drains non-hijacked handlers; hijacked conns are untracked, so it
	// returns fast and DrainWS handles the browser streams.
	err := s.http.Shutdown(ctx)
	api.DrainWS(ctx)
	// close the manager and store even when the drain times out
	cerr := s.Close()
	if err != nil {
		return err
	}
	return cerr
}

// metricsLoop refreshes the node and DB size gauges every 60s until the
// manager stops.
func metricsLoop(s *store.Store, cfg *config.Config, m *nodemanager.Manager) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	refresh := func() {
		if total, err := s.CountNodes(); err == nil {
			metrics.NodesTotal.Set(float64(total))
		}
		metrics.NodesOnline.Set(float64(m.Count()))
		var size int64
		for _, p := range []string{cfg.DBPath, cfg.DBPath + "-wal", cfg.DBPath + "-shm"} {
			if fi, err := os.Stat(p); err == nil {
				size += fi.Size()
			}
		}
		metrics.DBSize.Set(float64(size))
	}
	for {
		refresh()
		select {
		case <-m.Done():
			return
		case <-t.C:
		}
	}
}

// bootstrap ensures a JWT secret and an admin user exist.
func bootstrap(s *store.Store, cfg *config.Config) error {
	if cfg.JWTSecret == "" {
		var err error
		cfg.JWTSecret, err = s.GetSetting("jwt_secret")
		if err != nil {
			return err
		}
		if cfg.JWTSecret == "" {
			cfg.JWTSecret = uuid.NewString() + uuid.NewString()
			if err := s.SetSetting("jwt_secret", cfg.JWTSecret); err != nil {
				return err
			}
		}
	}
	if cfg.AdminUsername == "" || cfg.AdminPassword == "" {
		return nil
	}
	n, err := s.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.CreateUser(cfg.AdminUsername, string(hash)); err != nil {
		return err
	}
	slog.Info("bootstrapped admin user", "username", cfg.AdminUsername)
	return nil
}

// periodicReconcile re-checks approved nodes every 15s so new approvals and
// crashes reconnect without manual triggers.
func periodicReconcile(m *nodemanager.Manager) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.Done():
			return
		case <-t.C:
			m.Reconcile()
		}
	}
}
