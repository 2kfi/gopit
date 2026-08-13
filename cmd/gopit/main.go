// gopit: the Gopit control plane. Serves the web dashboard and manages agent nodes.
package main

import (
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gopit/internal/server"
)

func main() {
	cfgPath := flag.String("config", "configs/gopit.yaml", "path to server YAML config")
	flag.Parse()

	srv, err := server.New(*cfgPath)
	if err != nil {
		slog.Error("server init failed", "err", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		srv.Close()
		os.Exit(0)
	}()

	if err := srv.Run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
