// Команда app запускает сервис котировок: HTTP API и работу с базой.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/config"
	"github.com/1lnazis/Test-Assignment/internal/httpapi"
	"github.com/1lnazis/Test-Assignment/internal/service"
	"github.com/1lnazis/Test-Assignment/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

// run собирает приложение и ждёт сигнала остановки. Вынесена из main, чтобы
// можно было вернуть ошибку, а defer при этом отработали до os.Exit.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	// ctx отменяется по Ctrl+C или SIGTERM (так останавливает контейнер Docker).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}

	svc := service.New(postgres.NewRepo(pool), cfg.Currencies)
	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: httpapi.New(svc, log).Routes(),
		// Таймауты защищают от медленных клиентов, которые держат соединения открытыми.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server started", "addr", cfg.HTTPAddr)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		// Сюда попадаем, только если сервер упал сам, например порт уже занят.
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Даём уже принятым запросам время завершиться, новые не принимаем.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
