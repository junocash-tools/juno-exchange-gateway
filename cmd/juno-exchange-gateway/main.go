package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Abdullah1738/juno-exchange-gateway/internal/adapters/addrgen"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/adapters/node"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/adapters/scanner"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/api"
	"github.com/Abdullah1738/juno-exchange-gateway/internal/config"
	storage "github.com/Abdullah1738/juno-exchange-gateway/internal/storage/sqlite"
)

var (
	version        = "dev"
	revision       = "unknown"
	buildTime      = ""
	componentsJSON = "{}"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration_invalid", "error", err.Error())
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := storage.Open(ctx, cfg.StateDSN)
	if err != nil {
		logger.Error("state_open_failed", "error", err.Error())
		os.Exit(1)
	}
	defer store.Close()

	nodeClient := node.New(cfg.NodeRPCURL, cfg.NodeRPCUser, cfg.NodeRPCPassword, cfg.UpstreamTimeout)
	scannerClient := scanner.New(cfg.ScannerURL, cfg.ScannerToken, cfg.UpstreamTimeout, cfg.BackfillTimeout)
	components := map[string]string{}
	if err := json.Unmarshal([]byte(componentsJSON), &components); err != nil {
		logger.Error("build_manifest_invalid")
		os.Exit(1)
	}
	service, err := api.New(cfg, store, nodeClient, scannerClient, addrgen.Deriver{Path: cfg.AddrgenPath}, logger, api.BuildInfo{
		Version: version, Revision: revision, BuildTime: buildTime, APIVersion: "v1", Components: components,
	})
	if err != nil {
		logger.Error("gateway_initialize_failed", "error", err.Error())
		os.Exit(1)
	}
	go service.ReconcileWallets(ctx)

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("gateway_started", "listen", cfg.ListenAddress, "network", cfg.Network, "version", version, "revision", revision)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("gateway_shutdown_failed", "error", err.Error())
			_ = server.Close()
		}
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("gateway_server_failed", "error", err.Error())
			os.Exit(1)
		}
	}
	logger.Info("gateway_stopped")
}
