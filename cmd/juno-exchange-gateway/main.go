package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/junocash-tools/juno-exchange-gateway/internal/adapters/addrgen"
	"github.com/junocash-tools/juno-exchange-gateway/internal/adapters/node"
	"github.com/junocash-tools/juno-exchange-gateway/internal/adapters/scanner"
	"github.com/junocash-tools/juno-exchange-gateway/internal/api"
	"github.com/junocash-tools/juno-exchange-gateway/internal/config"
	"github.com/junocash-tools/juno-exchange-gateway/internal/coordinator"
	"github.com/junocash-tools/juno-exchange-gateway/internal/installation"
	"github.com/junocash-tools/juno-exchange-gateway/internal/lifecycle"
	storage "github.com/junocash-tools/juno-exchange-gateway/internal/storage/sqlite"
)

var (
	version        = "dev"
	revision       = "unknown"
	buildTime      = ""
	componentsJSON = "{}"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	os.Exit(run(logger, os.Args[1:], os.Stdout, os.Stderr))
}

func run(logger *slog.Logger, args []string, stdout, stderr io.Writer) int {
	syscall.Umask(0o077)
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	if command != "serve" && command != "init" && command != "recovery-checksum" && command != "recover" {
		fmt.Fprintf(stderr, "unknown command %q; use serve, init, recovery-checksum, or recover\n", command)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration_invalid", "error", err.Error())
		return 2
	}

	switch command {
	case "init":
		return runInit(ctx, logger, cfg, args, stdout, stderr)
	case "recovery-checksum":
		return runRecoveryChecksum(ctx, logger, cfg, args, stdout, stderr)
	case "recover":
		return runRecover(ctx, logger, cfg, args, stdout, stderr)
	default:
		if len(args) != 0 {
			fmt.Fprintln(stderr, "serve does not accept arguments")
			return 2
		}
		return runServe(ctx, logger, cfg)
	}
}

func runInit(ctx context.Context, logger *slog.Logger, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	acknowledgement := flags.String("acknowledge", "", "required exact new-installation acknowledgement")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "init does not accept positional arguments")
		return 2
	}
	store, err := storage.Open(ctx, cfg.StateDSN)
	if err != nil {
		logger.Error("state_open_failed", "error", err.Error())
		return 1
	}
	defer store.Close()
	manifest, err := lifecycle.Initialize(ctx, cfg, store, addrgen.Deriver{Path: cfg.AddrgenPath}, *acknowledgement)
	if err != nil {
		logger.Error("installation_init_failed", "error", err.Error())
		return 1
	}
	return writeJSON(stdout, logger, map[string]any{
		"status":          "initialized",
		"installation_id": manifest.InstallationID,
		"network":         manifest.Network,
		"wallets":         len(manifest.Wallets),
	})
}

func runRecoveryChecksum(ctx context.Context, logger *slog.Logger, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("recovery-checksum", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var nextIndices nextIndexFlags
	flags.Var(&nextIndices, "next-index", "optional wallet_id=N recovery high-water; repeat per wallet")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "recovery-checksum does not accept positional arguments")
		return 2
	}
	_, manifest, err := installation.Open(cfg.InstallationStatePath, lifecycle.Identity(cfg))
	if err != nil {
		logger.Error("recovery_checksum_failed", "error", err.Error())
		return 1
	}
	targets, err := lifecycle.RecoveryTargets(manifest, nextIndices)
	if err != nil {
		logger.Error("recovery_checksum_failed", "error", err.Error())
		return 1
	}
	checksum, err := lifecycle.RecoveryChecksum(ctx, cfg, manifest, targets, addrgen.Deriver{Path: cfg.AddrgenPath})
	if err != nil {
		logger.Error("recovery_checksum_failed", "error", err.Error())
		return 1
	}
	return writeJSON(stdout, logger, map[string]any{
		"status":             "verified",
		"installation_id":    manifest.InstallationID,
		"network":            manifest.Network,
		"next_address_index": targets,
		"mapping_sha256":     checksum,
	})
}

func runRecover(ctx context.Context, logger *slog.Logger, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("recover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	acknowledgement := flags.String("acknowledge", "", "required exact recovery acknowledgement")
	expectedChecksum := flags.String("mapping-sha256", "", "expected checksum emitted by recovery-checksum")
	var nextIndices nextIndexFlags
	flags.Var(&nextIndices, "next-index", "optional wallet_id=N recovery high-water; repeat per wallet")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "recover does not accept positional arguments")
		return 2
	}
	store, err := storage.Open(ctx, cfg.StateDSN)
	if err != nil {
		logger.Error("state_open_failed", "error", err.Error())
		return 1
	}
	defer store.Close()
	manifest, err := lifecycle.Recover(ctx, cfg, store, addrgen.Deriver{Path: cfg.AddrgenPath}, *acknowledgement, *expectedChecksum, nextIndices)
	if err != nil {
		logger.Error("installation_recovery_failed", "error", err.Error())
		return 1
	}
	return writeJSON(stdout, logger, map[string]any{
		"status":          "recovered",
		"installation_id": manifest.InstallationID,
		"network":         manifest.Network,
		"mapping_sha256":  *expectedChecksum,
	})
}

func runServe(ctx context.Context, logger *slog.Logger, cfg config.Config) int {
	rawStore, err := storage.Open(ctx, cfg.StateDSN)
	if err != nil {
		logger.Error("state_open_failed", "error", err.Error())
		return 1
	}
	defer rawStore.Close()
	store, manifest, err := lifecycle.OpenForServe(ctx, cfg, rawStore)
	if err != nil {
		logger.Error("gateway_initialize_failed", "error", err.Error())
		return 1
	}

	nodeClient := node.New(cfg.NodeRPCURL, cfg.NodeRPCUser, cfg.NodeRPCPassword, cfg.UpstreamTimeout)
	scannerClient := scanner.New(cfg.ScannerURL, cfg.ScannerToken, cfg.UpstreamTimeout, cfg.BackfillTimeout)
	components := map[string]string{}
	if err := json.Unmarshal([]byte(componentsJSON), &components); err != nil {
		logger.Error("build_manifest_invalid")
		return 1
	}
	service, err := api.New(cfg, store, nodeClient, scannerClient, addrgen.Deriver{Path: cfg.AddrgenPath}, logger, api.BuildInfo{
		Version: version, Revision: revision, BuildTime: buildTime, APIVersion: "v1", Components: components,
	})
	if err != nil {
		logger.Error("gateway_initialize_failed", "error", err.Error())
		return 1
	}
	go service.ReconcileWallets(ctx)

	publicServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           service.Handler(),
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    32 << 10,
	}
	type serverResult struct {
		name string
		err  error
	}
	errCh := make(chan serverResult, 2)
	go func() {
		logger.Info("gateway_started", "listen", cfg.ListenAddress, "network", cfg.Network, "installation_id", manifest.InstallationID, "version", version, "revision", revision)
		errCh <- serverResult{name: "gateway", err: publicServer.ListenAndServe()}
	}()

	var coordinatorServer *http.Server
	if cfg.CoordinatorEnabled {
		signer := coordinator.NewUnixSigner(cfg.CoordinatorSignerSocket, cfg.CoordinatorSignTimeout)
		coordinatorService, err := coordinator.NewService(
			cfg,
			store,
			nodeClient,
			scannerClient,
			service.AllocateInternalAddress,
			coordinator.NewExecPlanner(cfg),
			signer,
			logger,
		)
		if err != nil {
			logger.Error("coordinator_initialize_failed", "error", err.Error())
			return 1
		}
		coordinatorHandler, err := coordinator.NewHandler(cfg, coordinatorService)
		if err != nil {
			logger.Error("coordinator_initialize_failed", "error", err.Error())
			return 1
		}
		coordinatorService.Start(ctx)
		coordinatorServer = &http.Server{
			Addr:              cfg.CoordinatorListenAddress,
			Handler:           coordinatorHandler,
			ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
			MaxHeaderBytes:    32 << 10,
		}
		go func() {
			logger.Info("coordinator_started", "listen", cfg.CoordinatorListenAddress, "network", cfg.Network)
			errCh <- serverResult{name: "coordinator", err: coordinatorServer.ListenAndServe()}
		}()
	}

	failed := false
	select {
	case <-ctx.Done():
	case result := <-errCh:
		if result.err == nil || !errors.Is(result.err, http.ErrServerClosed) {
			message := "server stopped unexpectedly"
			if result.err != nil {
				message = result.err.Error()
			}
			logger.Error(result.name+"_server_failed", "error", message)
			failed = true
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	for name, server := range map[string]*http.Server{"gateway": publicServer, "coordinator": coordinatorServer} {
		if server == nil {
			continue
		}
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error(name+"_shutdown_failed", "error", err.Error())
			_ = server.Close()
			failed = true
		}
	}
	logger.Info("gateway_stopped")
	if failed {
		return 1
	}
	return 0
}

type nextIndexFlags map[string]uint64

func (f *nextIndexFlags) String() string {
	if f == nil || len(*f) == 0 {
		return ""
	}
	parts := make([]string, 0, len(*f))
	for walletID, index := range *f {
		parts = append(parts, fmt.Sprintf("%s=%d", walletID, index))
	}
	return strings.Join(parts, ",")
}

func (f *nextIndexFlags) Set(raw string) error {
	walletID, indexRaw, ok := strings.Cut(strings.TrimSpace(raw), "=")
	if !ok || strings.TrimSpace(walletID) == "" || strings.TrimSpace(indexRaw) == "" {
		return errors.New("next-index must use wallet_id=N")
	}
	index, err := strconv.ParseUint(indexRaw, 10, 64)
	if err != nil {
		return errors.New("next-index must use a non-negative integer")
	}
	if *f == nil {
		*f = make(nextIndexFlags)
	}
	if _, exists := (*f)[walletID]; exists {
		return fmt.Errorf("next-index for wallet %q was provided more than once", walletID)
	}
	(*f)[walletID] = index
	return nil
}

func writeJSON(out io.Writer, logger *slog.Logger, value any) int {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		logger.Error("write_command_output_failed", "error", err.Error())
		return 1
	}
	return 0
}

func flagExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
