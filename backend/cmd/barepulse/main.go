package main

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ygandwx/BarePulse/backend/internal/app"
	"github.com/ygandwx/BarePulse/backend/migrations"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("BarePulse stopped", "error", e.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return errors.New("invalid database configuration")
	}
	defer db.Close()
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if e = db.Ping(check); e != nil {
		return errors.New("database unavailable")
	}
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "migrate" {
		return migrations.Apply(ctx, db)
	}
	var count int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); e != nil {
		return errors.New("database not migrated; run barepulse migrate first")
	}
	cfg := app.Config{BaseURL: env("BASE_URL", "http://127.0.0.1:8080"), SecureCookies: os.Getenv("SECURE_COOKIES") == "true", AllowRealDevices: os.Getenv("ALLOW_REAL_DEVICES") == "true", AllowDeviceWrites: os.Getenv("ALLOW_DEVICE_WRITES") == "true", MediaDir: env("MEDIA_DIR", "work/media")}
	if path := os.Getenv("BAREPULSE_DEVICE_PROFILES_FILE"); path != "" {
		cfg.PowerProfiles, e = app.LoadPowerProfiles(path)
		if e != nil {
			return e
		}
	}
	if v := os.Getenv("MAX_MEDIA_BYTES"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n < 1 || n > 2_000_000_000 {
			return errors.New("MAX_MEDIA_BYTES must be 1..2000000000")
		}
		cfg.MaxMediaBytes = n
	}
	a := app.New(db, cfg)
	if mode == "bootstrap" {
		return a.Bootstrap(ctx, os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD"))
	}
	if mode != "serve" {
		return errors.New("usage: barepulse [serve|migrate|bootstrap]")
	}
	go a.RunWorker(ctx)
	go a.RunTrafficCollector(ctx)
	go a.RunConsole(ctx)
	srv := &http.Server{Addr: env("LISTEN_ADDR", "127.0.0.1:8080"), Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("BarePulse listening", "address", srv.Addr, "real_devices", cfg.AllowRealDevices, "device_writes", cfg.AllowDeviceWrites)
	e = srv.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
