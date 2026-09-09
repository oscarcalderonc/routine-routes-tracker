// Command tracker imports recorded drives and reports how long each stretch of
// the route takes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/config"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/drive"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/service"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/storage"
	"github.com/oscarcalderonc/routine-routes-tracker/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

// ensureDataDir checks that the data directory exists and can be written to,
// before anything tries to use it.
//
// In deployment this directory is a bind mount from the host, and a bind mount
// takes its ownership from the host rather than from the image. If it has not
// been given to the user the container runs as, every later failure is a
// confusing one from whichever component touches the filesystem first, so the
// condition is reported here in terms of the fix.
func ensureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("data directory %s cannot be created (uid %d): %w", dir, os.Getuid(), err)
	}

	probe := filepath.Join(dir, ".write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("data directory %s is not writable by uid %d; "+
			"if it is a bind mount, run: chown -R %d:%d %s: %w",
			dir, os.Getuid(), os.Getuid(), os.Getgid(), dir, err)
	}
	return os.Remove(probe)
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// The background context outlives individual requests, so work a request
	// starts — importing a backlog, recomputing after a route change — is not
	// abandoned when the browser navigates away. It ends on shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := ensureDataDir(cfg.DataDir); err != nil {
		return err
	}
	if err := drive.WriteConfig(cfg.RcloneConfig, cfg.RcloneConfigB64); err != nil {
		return err
	}

	store, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	svc, err := service.New(service.Options{
		Store: store,
		Puller: drive.Puller{
			Bin:    cfg.RcloneBin,
			Config: cfg.RcloneConfig,
			Remote: cfg.DriveRemote,
			Folder: cfg.DriveFolder,
			Dest:   cfg.InboxDir(),
		},
		Location: cfg.Location,
		Anchor:   cfg.Anchor,
		BlobDir:  cfg.BlobDir(),
		InboxDir: cfg.InboxDir(),
		MaxBytes: cfg.MaxUploadBytes,
		Logger:   log,
	})
	if err != nil {
		return err
	}

	// A route may have been edited, or the matching algorithm changed, while
	// the tracker was not running. Bringing everything up to date on startup
	// means the figures on screen always reflect the current definitions.
	go func() {
		if err := svc.Reprocess(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("startup recompute failed", "error", err)
		}
	}()

	srv, err := web.NewServer(ctx, svc, log)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Importing a backlog can take a while, so the write timeout is
		// generous rather than absent.
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  90 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()

	log.Info("listening",
		"addr", cfg.Addr,
		"timezone", cfg.Location.String(),
		"anchor", string(cfg.Anchor),
		"drive", cfg.DriveRemote != "")

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
