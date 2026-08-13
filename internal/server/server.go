// Package server orchestrates the gopit server: config, store, discovery,
// node manager and HTTP API.
package server

import (
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gopit/internal/protocol"
	"gopit/internal/server/api"
	"gopit/internal/server/config"
	"gopit/internal/server/discovery"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"

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
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := bootstrap(s, cfg); err != nil {
		s.Close()
		return nil, err
	}
	m := nodemanager.New(s, nodemanager.Callbacks{
		OnStatus: func(nodeID, status string) {
			s.SetNodeStatus(nodeID, status)
		},
		OnStats: func(nodeID string, stats protocol.SystemStats) {}, // live relay goes via WS subscribers
	}, cfg.TLSSkipVerify)
	srv := &Server{cfg: cfg, store: s, manager: m}

	disc := &discovery.Client{
		BroadcastAddr: cfg.DiscoveryBroadcast,
		Port:          cfg.DiscoveryPort,
		Timeout:       500 * time.Millisecond,
	}
	handler := api.Router(s, m, disc, cfg.JWTSecret, cfg.TLSSkipVerify)
	srv.http = &http.Server{Addr: cfg.ListenAddr, Handler: handler}
	go m.Reconcile()
	go periodicReconcile(m)
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
	for {
		time.Sleep(15 * time.Second)
		m.Reconcile()
	}
}
