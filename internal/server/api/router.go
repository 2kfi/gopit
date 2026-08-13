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
	"gopit/internal/server/nodemanager"
	"gopit/internal/server/store"
)

// Router builds the full HTTP router: API + embedded SPA.
func Router(s *store.Store, m *nodemanager.Manager, disc *discovery.Client, secret string, tlsSkipVerify bool) http.Handler {
	r := chi.NewRouter()
	auth := NewAuth(s, secret)
	authAPI := NewAuthAPI(s, auth)
	nodesAPI := NewNodesAPI(s, m, *disc)
	statusAPI := NewStatusAPI(m, s)
	dockerAPI := NewDockerAPI(m)
	firewallAPI := NewFirewallAPI(m)
	terminalAPI := NewTerminalAPI(s, tlsSkipVerify)

	r.Route("/api", func(r chi.Router) {
		r.Post("/login", authAPI.Login)
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware)
			r.Post("/logout", authAPI.Logout)
			r.Put("/password", authAPI.ChangePassword)

			r.Get("/nodes", nodesAPI.List)
			r.Post("/nodes/discover", nodesAPI.Discover)
			r.Post("/nodes", nodesAPI.Create)
			r.Post("/nodes/{uuid}/approve", nodesAPI.Approve)
			r.Post("/nodes/{uuid}/token", nodesAPI.SetToken)
			r.Delete("/nodes/{uuid}", nodesAPI.Delete)

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
			r.Post("/nodes/{uuid}/compose/deploy", dockerAPI.ComposeDeploy)
			r.Post("/nodes/{uuid}/compose/{name}/down", dockerAPI.ComposeDown)
			r.Get("/nodes/{uuid}/compose/{name}/ps", dockerAPI.ComposePS)

			r.Get("/nodes/{uuid}/firewall", firewallAPI.Status)
			r.Post("/nodes/{uuid}/firewall/rules", firewallAPI.AddRule)
			r.Delete("/nodes/{uuid}/firewall/rules/{number}", firewallAPI.DeleteRule)
			r.Post("/nodes/{uuid}/firewall/toggle", firewallAPI.Toggle)

			r.Group(func(r chi.Router) {
				r.Use(auth.AdminOnly)
				r.Post("/users", authAPI.CreateUser)
				r.Get("/users", authAPI.ListUsers)
				r.Delete("/users/{id}", authAPI.DeleteUser)
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
