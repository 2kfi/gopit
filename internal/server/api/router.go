package api

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	webui "gopit" // root package: embeds web/dist
	"gopit/internal/server/discovery"
	"gopit/internal/server/metrics"
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
	"gopit/internal/server/webhooks"
)

// Version is the server build version; stamp at build time via
// -ldflags "-X gopit/internal/server/api.Version=x.y.z".
var Version = "dev"

// Router builds the full HTTP router: API + embedded SPA.
func Router(s *store.Store, m *nodemanager.Manager, disc *discovery.Client, secret, pairToken string, tlsSkipVerify, trustProxy bool, rateLimitPerMin int, passwordMinScore int, hooks *webhooks.Store) http.Handler {
	trustXFF = trustProxy
	r := chi.NewRouter()
	r.Use(metrics.Middleware)
	auth := NewAuth(s, secret)
	rl := NewRateLimiter(rateLimitPerMin, rateLimitPerMin/5+1)
	authAPI := NewAuthAPI(s, auth, passwordMinScore, hooks)
	nodesAPI := NewNodesAPI(s, m, *disc, pairToken)
	statusAPI := NewStatusAPI(m, s)
	dockerAPI := NewDockerAPI(m, hooks)
	firewallAPI := NewFirewallAPI(m, hooks)
	terminalAPI := NewTerminalAPI(s, tlsSkipVerify)

	// /metrics is unauthenticated (scraped by Prometheus); keep it outside
	// /api so it skips auth, CSRF and rate limiting. It exposes node counts,
	// live WS gauge and version — no secrets — but firewall it at the proxy
	// if that metadata must not leak.
	r.Get("/metrics", metrics.Handler().ServeHTTP)

	r.Route("/api", func(r chi.Router) {
		r.Use(rl.Middleware)
		r.Use(originCheck)
		r.Use(auth.AuditLog)
		// /api/health: unauthenticated liveness for LB/k8s probes. The DB
		// ping plus node counts reflect real readiness; version aids
		// rollback verification. Failures answer 503 so probes drain us.
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			total, err := s.CountNodes()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"db": "error", "error": err.Error(),
					"nodes_online": 0, "nodes_total": 0, "version": Version,
				})
				return
			}
			resp := map[string]any{
				"db": "ok", "nodes_online": m.Count(), "nodes_total": total, "version": Version,
			}
			if err := s.Ping(); err != nil {
				resp["db"] = "error"
				writeJSON(w, http.StatusServiceUnavailable, resp)
				return
			}
			writeJSON(w, http.StatusOK, resp)
		})
		r.Get("/setup/status", authAPI.SetupStatus)
		r.Post("/setup", authAPI.Setup)
		r.Post("/login", authAPI.Login)
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware)
			r.Use(CSRFMiddleware(auth))
			r.Get("/me", authAPI.Me)
			r.Post("/logout", authAPI.Logout)
			r.Put("/password", authAPI.ChangePassword)

			r.Get("/nodes", nodesAPI.List)
			r.Post("/nodes/discover", nodesAPI.Discover)
			r.Post("/nodes", nodesAPI.Create)
			r.Post("/nodes/{uuid}/approve", nodesAPI.Approve)
			r.Post("/nodes/{uuid}/token", nodesAPI.SetToken)
			r.Delete("/nodes/{uuid}", nodesAPI.Delete)

			r.Get("/wizard", nodesAPI.WizardInfo)
			r.Post("/wizard/done", nodesAPI.WizardDone)

			r.Get("/nodes/{uuid}/status", statusAPI.Stream)
			r.Get("/nodes/{uuid}/terminal", terminalAPI.Stream)

			r.Get("/nodes/{uuid}/containers", dockerAPI.Containers)
			r.Get("/nodes/{uuid}/containers/{id}", dockerAPI.Inspect)
			r.Post("/nodes/{uuid}/containers/{id}/start", dockerAPI.Start)
			r.Post("/nodes/{uuid}/containers/{id}/stop", dockerAPI.Stop)
			r.Delete("/nodes/{uuid}/containers/{id}", dockerAPI.Remove)
			r.Get("/nodes/{uuid}/containers/{id}/logs", dockerAPI.Logs)
			r.Get("/nodes/{uuid}/images", dockerAPI.Images)
			r.Delete("/nodes/{uuid}/images/{id}", dockerAPI.RemoveImage)
			r.Get("/nodes/{uuid}/volumes", dockerAPI.Volumes)
			r.Delete("/nodes/{uuid}/volumes/{id}", dockerAPI.RemoveVolume)
			r.Get("/nodes/{uuid}/compose", dockerAPI.ComposeStacks)
			r.Post("/nodes/{uuid}/compose/validate", dockerAPI.ComposeValidate)
			r.Post("/nodes/{uuid}/compose/deploy", dockerAPI.ComposeDeploy)
			r.Post("/nodes/{uuid}/compose/{name}/down", dockerAPI.ComposeDown)
			r.Get("/nodes/{uuid}/compose/{name}/ps", dockerAPI.ComposePS)

			r.Get("/nodes/{uuid}/firewall", firewallAPI.Status)
			r.Post("/nodes/{uuid}/firewall/preview", firewallAPI.Preview)
			r.Post("/nodes/{uuid}/firewall/rules", firewallAPI.AddRule)
			r.Delete("/nodes/{uuid}/firewall/rules/{number}", firewallAPI.DeleteRule)
			r.Post("/nodes/{uuid}/firewall/toggle", firewallAPI.Toggle)

			r.Group(func(r chi.Router) {
				r.Use(auth.AdminOnly)
				r.Post("/users", authAPI.CreateUser)
				r.Get("/users", authAPI.ListUsers)
				r.Delete("/users/{id}", authAPI.DeleteUser)
				r.Post("/admin/jwt/rotate", authAPI.RotateJWT)
			})
		})
	})

	spa(r)
	return r
}

// spa serves the embedded frontend with index.html fallback.
func spa(r chi.Router) {
	dist, err := fs.Sub(webui.FS, "web/dist")
	if err != nil {
		panic("web/dist missing: run `npm run build` in web/ before building the server: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(dist))
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		p := strings.TrimPrefix(req.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(dist, p); err == nil {
				fileServer.ServeHTTP(w, req)
				return
			}
		}
		// SPA fallback
		index, err := dist.Open("index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer index.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, req, "index.html", time.Now(), index.(io.ReadSeeker))
	})
}
