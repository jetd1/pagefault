package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jet/pagefault/internal/audit"
	"github.com/jet/pagefault/internal/auth"
	"github.com/jet/pagefault/internal/backend"
	"github.com/jet/pagefault/internal/config"
	"github.com/jet/pagefault/internal/dispatcher"
	"github.com/jet/pagefault/internal/filter"
	"github.com/jet/pagefault/internal/server"
)

// runServe parses flags, loads config, wires every subsystem together, and
// starts the HTTP server. It blocks until a shutdown signal is received.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to pagefault.yaml")
	hostOverride := fs.String("host", "", "override server host (default from config)")
	portOverride := fs.Int("port", 0, "override server port (default from config)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return errors.New("--config is required")
	}

	// Slog to stderr with text handler — friendly for terminal output.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	server.Version = version

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *hostOverride != "" {
		cfg.Server.Host = *hostOverride
	}
	if *portOverride > 0 {
		cfg.Server.Port = *portOverride
	}

	d, closer, err := buildDispatcher(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if closer != nil {
			_ = closer()
		}
	}()

	provider, err := auth.NewProvider(cfg.Auth)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	s, err := server.New(cfg, d, provider)
	if err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("pagefault listening",
			"addr", addr,
			"auth", cfg.Auth.Mode,
			"backends", d.SortedBackendNames(),
			"version", version,
		)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
	return nil
}

// buildDispatcher constructs backends, filter, and audit logger from config,
// then returns a wired-up dispatcher along with a closer that releases
// resources on shutdown.
func buildDispatcher(cfg *config.Config) (*dispatcher.ToolDispatcher, func() error, error) {
	var backends []backend.Backend
	for _, bc := range cfg.Backends {
		switch bc.Type {
		case "filesystem":
			fsCfg, err := config.DecodeFilesystemBackend(bc)
			if err != nil {
				return nil, nil, err
			}
			be, err := backend.NewFilesystemBackend(fsCfg)
			if err != nil {
				return nil, nil, err
			}
			backends = append(backends, be)
		default:
			// Phase 1 only supports filesystem. Other types will be added
			// in Phase 2 (subprocess, http, subagent).
			return nil, nil, fmt.Errorf("backend %q: unsupported type %q (only 'filesystem' is available in Phase 1)", bc.Name, bc.Type)
		}
	}

	f, err := filter.NewFromConfig(cfg.Filters)
	if err != nil {
		return nil, nil, fmt.Errorf("filter: %w", err)
	}

	auditLog, err := audit.NewFromConfig(cfg.Audit)
	if err != nil {
		return nil, nil, fmt.Errorf("audit: %w", err)
	}

	d, err := dispatcher.New(dispatcher.Options{
		Backends: backends,
		Contexts: cfg.Contexts,
		Filter:   f,
		Audit:    auditLog,
		Tools:    cfg.Tools,
	})
	if err != nil {
		_ = auditLog.Close()
		return nil, nil, err
	}

	closer := func() error {
		return auditLog.Close()
	}
	return d, closer, nil
}
