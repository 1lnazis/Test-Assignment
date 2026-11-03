// Команда app запускает HTTP API и фоновый воркер в одном процессе.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/config"
	"github.com/1lnazis/Test-Assignment/internal/httpapi"
	"github.com/1lnazis/Test-Assignment/internal/provider/frankfurter"
	"github.com/1lnazis/Test-Assignment/internal/service"
	"github.com/1lnazis/Test-Assignment/internal/storage/postgres"
	"github.com/1lnazis/Test-Assignment/internal/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

// run собирает зависимости. Возврат ошибки позволяет defer отработать до os.Exit.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close() // runServices сначала дожидается HTTP и воркера.
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	repo := postgres.NewRepo(pool)
	provider, err := frankfurter.New(frankfurter.DefaultBaseURL, cfg.Worker.FetchTimeout)
	if err != nil {
		return err
	}
	background, err := worker.New(repo, provider, log, cfg.Worker)
	if err != nil {
		return err
	}
	svc := service.New(repo, cfg.Currencies)
	srv := &http.Server{
		Addr: cfg.HTTPAddr, Handler: httpapi.New(svc, log).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second, WriteTimeout: 10 * time.Second,
		IdleTimeout: 60 * time.Second,
	}
	// Проверяем порт до запуска обработчиков: при занятом порте джобы не захватываем.
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	log.Info("service started", "addr", listener.Addr().String(), "workers", cfg.Worker.Count)
	return runServices(ctx, srv, listener, background, cfg.ShutdownTimeout, log)
}
