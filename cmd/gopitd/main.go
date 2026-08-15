// gopitd: the Gopit node agent. Runs the UDP presence beacon and the
// WebSocket control plane on the same port.
package main

import (
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gopit/internal/agent"
	"gopit/internal/configresolve"
)

func main() {
	cfgPath := flag.String("config", "", "path to agent YAML config (default: configs/gopitd.yaml, then ~/.config/gopit/gopitd.yaml)")
	logFormat := flag.String("log-format", "text", "log format: text|json")
	flag.Parse()
	if *logFormat == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}

	path, err := configresolve.ResolveAgent(*cfgPath)
	if err != nil {
		slog.Error("config resolve failed", "err", err)
		os.Exit(1)
	}

	a, err := agent.New(path)
	if err != nil {
		slog.Error("agent init failed", "err", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		a.Close()
		os.Exit(0)
	}()

	if err := a.Run(); err != nil {
		slog.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}
