// gopit: the Gopit control plane. Serves the web dashboard and manages agent nodes.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopit/internal/server"
	serverconfig "gopit/internal/server/config"
	"gopit/internal/server/store"
)

func main() {
	setupLogging := func(fs *flag.FlagSet) {
		logFormat := fs.String("log-format", "text", "log format: text|json")
		fs.Parse(os.Args[2:])
		if *logFormat == "json" {
			slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		}
	}
	// Subcommands (run them before flag.Parse so serve stays the default):
	//   gopit backup > file.sql    dump the SQLite DB as SQL text to stdout
	//   gopit restore < file.sql   load a dump into the DB (stop the service first)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup":
			fs := flag.NewFlagSet("backup", flag.ExitOnError)
			cfgPath := fs.String("config", "configs/gopit.yaml", "path to server YAML config")
			setupLogging(fs)
			cfg, err := serverconfig.Load(*cfgPath)
			if err != nil {
				slog.Error("backup: config load failed", "err", err)
				os.Exit(1)
			}
			// stdout carries only SQL: errors go to stderr via slog
			if err := store.Dump(cfg.DBPath, os.Stdout); err != nil {
				slog.Error("backup failed", "err", err)
				os.Exit(1)
			}
			return
		case "restore":
			fs := flag.NewFlagSet("restore", flag.ExitOnError)
			cfgPath := fs.String("config", "configs/gopit.yaml", "path to server YAML config")
			setupLogging(fs)
			cfg, err := serverconfig.Load(*cfgPath)
			if err != nil {
				slog.Error("restore: config load failed", "err", err)
				os.Exit(1)
			}
			slog.Warn("restore: stop gopit.service first; the running server holds the DB open")
			if err := store.Restore(cfg.DBPath, os.Stdin); err != nil {
				slog.Error("restore failed", "err", err)
				os.Exit(1)
			}
			return
		}
	}

	cfgPath := flag.String("config", "configs/gopit.yaml", "path to server YAML config")
	drainTimeout := flag.Duration("drain-timeout", 10*time.Second, "how long to wait for active WebSocket sessions on shutdown")
	logFormat := flag.String("log-format", "text", "log format: text|json")
	flag.Parse()
	if *logFormat == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}

	srv, err := server.New(*cfgPath)
	if err != nil {
		slog.Error("server init failed", "err", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		// drain in-flight HTTP handlers and WS sessions, then stop manager +
		// store; the context caps the drain so the process still exits promptly.
		ctx, cancel := context.WithTimeout(context.Background(), *drainTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("shutdown failed", "err", err)
		}
		os.Exit(0)
	}()

	if err := srv.Run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
