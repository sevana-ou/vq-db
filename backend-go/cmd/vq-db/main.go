// Command vq-db is the Go daemon entrypoint. It assembles the tested components
// into a running service:
//
//	ZMQ SUB -> IngestPipeline -> Writer (+ active-stream registry)
//	net/http (read API + /track control + static frontend)
//
// Usage: vq-db --config /etc/vq-monitor/vq-monitor.cfg
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/sevana-ou/vq-db/internal/api"
	"github.com/sevana-ou/vq-db/internal/bus"
	"github.com/sevana-ou/vq-db/internal/config"
	"github.com/sevana-ou/vq-db/internal/db"
	"github.com/sevana-ou/vq-db/internal/ingest"
	"github.com/sevana-ou/vq-db/internal/model"
	"github.com/sevana-ou/vq-db/internal/state"
	"github.com/sevana-ou/vq-db/internal/web"
	"github.com/sevana-ou/vq-db/internal/worker"
)

// version is the vq-db backend's own version. It is a var (not a const) so the
// build can stamp the real version via -ldflags "-X main.version=…" (see
// scripts/build_bundle.py); the default here tracks backend-go/VERSION.
var version = "2.1.9"

func main() {
	os.Exit(run())
}

func run() int {
	cfgPath := flag.String("config", "", "path to vq-monitor.cfg (YAML)")
	host := flag.String("host", "", "override dashboard.host from config (default 0.0.0.0)")
	logLevel := flag.String("log-level", "", "override logging.level from config")
	flag.Parse()

	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}
	if *host != "" {
		cfg.DashboardHost = *host
	}
	if *logLevel != "" {
		cfg.LogLevel = *logLevel
	}
	configureLogging(cfg)
	writePidfile(cfg.Pidfile)

	if !cfg.HasDatabase() {
		slog.Error("config error: database engine/connection is required")
		return 1
	}

	dbTarget := db.SafeConnDescription(cfg.DBEngine, cfg.DBConnection)
	slog.Info("connecting to database", "engine", cfg.DBEngine, "target", dbTarget)
	conn, err := db.Open(cfg.DBEngine, cfg.DBConnection)
	if err != nil {
		slog.Error("database connection failed", "engine", cfg.DBEngine, "target", dbTarget, "err", err)
		return 1
	}
	defer conn.Close()

	writer := db.NewWriter(conn, cfg.AgentID, cfg.AgentName)
	if err := writer.Bootstrap(); err != nil {
		slog.Error("database connected but schema bootstrap failed", "engine", cfg.DBEngine, "target", dbTarget, "err", err)
		return 1
	}
	if dbTarget == ":memory:" {
		slog.Info("database connection established (in-memory: volatile, not persisted)", "engine", cfg.DBEngine, "target", dbTarget)
	} else {
		slog.Info("database connection established", "engine", cfg.DBEngine, "target", dbTarget)
	}
	if cfg.RecordsLimit > 0 {
		writer.SetRecordsLimit(cfg.RecordsLimit)
	}
	if len(cfg.StoreSipFilter) > 0 {
		writer.SetStoreSipFilter(cfg.StoreSipFilter)
	}
	if cfg.RecordsLimit > 0 || len(cfg.StoreSipFilter) > 0 {
		slog.Info("bounded retention enabled", "records_limit", cfg.RecordsLimit, "store_sip_filter", cfg.StoreSipFilter)
	}

	registry := state.NewActiveStreamRegistry(cfg.AgentID, cfg.AgentName)
	snapshot := state.NewInstanceSnapshot()
	pipeline := ingest.NewPipeline(writer, registry, func(s model.InstanceStatistics) { snapshot.Set(s) })

	// Idle-timeout ghost sweep runs on the bus goroutine (via on_idle) so it can
	// safely drive the single-owner writer.
	var onIdle func()
	if cfg.GhostStreamTimeoutS > 0 {
		sweeper := worker.NewGhostSweeper(writer, registry, int64(cfg.GhostStreamTimeoutS), 0)
		onIdle = sweeper.Tick
	}
	subscriber := bus.SubscriberForPort(cfg.ZeroMQPort, pipeline.OnBytes, onIdle)

	control, err := bus.ControlClientForPort(cfg.ZeroMQControlPort, 5*time.Second)
	if err != nil {
		slog.Warn("could not open control socket", "err", err)
	}
	trackStore := db.NewTrackStore(writer)

	deps := api.Deps{
		DB:                    conn,
		AgentID:               cfg.AgentID,
		AgentName:             cfg.AgentName,
		GoodMosThreshold:      cfg.GoodMosThreshold,
		SilenceRatioThreshold: cfg.SilenceRatioThreshold,
		MaxStreams:            cfg.MaxStreams,
		Version:               version,
		Registry:              registry,
		Snapshot:              snapshot,
		TrackStore:            trackStore,
	}
	if control != nil {
		deps.Control = control
	}
	if cfg.DashboardRoot != "" {
		slog.Info("dashboard.root is deprecated and ignored — the dashboard is embedded in the binary", "root", cfg.DashboardRoot)
	}
	deps.UI = web.NewApp(deps).Handler()
	app := api.NewApp(deps)

	// Start the bus + workers.
	if err := subscriber.Start(); err != nil {
		slog.Error("failed to start subscriber", "err", err)
		return 1
	}

	var cleanup *worker.CleanupWorker
	if cfg.RecordsLifetimeS > 0 || cfg.AudioLifetimeS > 0 {
		cleanup = worker.NewCleanupWorker(writer, int64(cfg.RecordsLifetimeS), int64(cfg.AudioLifetimeS), 0)
		cleanup.Start()
	}

	if control != nil {
		worker.RestoreTrackPatterns(trackStore, control)
	}
	var trackSync *worker.TrackSyncWorker
	if control != nil && cfg.TrackResyncIntervalS > 0 {
		trackSync = worker.NewTrackSyncWorker(trackStore, control, time.Duration(cfg.TrackResyncIntervalS)*time.Second)
		trackSync.Start()
	}

	server := &http.Server{
		Addr:    net.JoinHostPort(cfg.DashboardHost, strconv.Itoa(cfg.DashboardPort)),
		Handler: app.Handler(),
	}

	slog.Info("vq-db started",
		"sub_port", cfg.ZeroMQPort, "control_port", cfg.ZeroMQControlPort, "dashboard_host", cfg.DashboardHost, "dashboard_port", cfg.DashboardPort)

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case err := <-serverErr:
		slog.Error("http server error", "err", err)
	}

	subscriber.Stop(2 * time.Second)
	if cleanup != nil {
		cleanup.Stop()
	}
	if trackSync != nil {
		trackSync.Stop()
	}
	if control != nil {
		control.Close()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	return 0
}

var logLevels = map[string]slog.Level{
	"critical": slog.LevelError,
	"error":    slog.LevelError,
	"warning":  slog.LevelWarn,
	"info":     slog.LevelInfo,
	"debug":    slog.LevelDebug,
	"media":    slog.LevelDebug,
}

func configureLogging(cfg *config.Config) {
	level, ok := logLevels[cfg.LogLevel]
	if !ok {
		level = slog.LevelInfo
	}
	var out *os.File = os.Stderr
	if cfg.LogFile != "" {
		if f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			out = f
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})))
}

func writePidfile(path string) {
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		slog.Warn("could not write pidfile", "path", path, "err", err)
	}
}
