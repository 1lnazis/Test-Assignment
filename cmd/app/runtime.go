package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type runner interface{ Run(context.Context) error }

// runServices владеет обеими горутинами и возвращается после их завершения.
func runServices(ctx context.Context, srv *http.Server, listener net.Listener, background runner, timeout time.Duration, log *slog.Logger) error {
	workerCtx, cancelWorker := context.WithCancel(ctx)
	defer cancelWorker()
	serverDone := make(chan error, 1)
	workerDone := make(chan error, 1)
	go func() { serverDone <- srv.Serve(listener) }()
	go func() { workerDone <- background.Run(workerCtx) }()

	var result error
	serverRead, workerRead := false, false
	select {
	case <-ctx.Done():
	case err := <-serverDone:
		serverRead = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("http server: %w", err)
		} else if ctx.Err() == nil {
			result = errors.New("http server stopped unexpectedly")
		}
	case err := <-workerDone:
		workerRead = true
		if err != nil {
			result = fmt.Errorf("worker: %w", err)
		} else if ctx.Err() == nil {
			result = errors.New("worker stopped unexpectedly")
		}
	}
	log.Info("shutting down")
	cancelWorker()
	// Один дедлайн на остановку HTTP и ожидание воркера.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, fmt.Errorf("http shutdown: %w", err))
		_ = srv.Close()
	}
	if !serverRead {
		if err := <-serverDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, fmt.Errorf("http server: %w", err))
		}
	}
	if !workerRead {
		select {
		case err := <-workerDone:
			if err != nil {
				result = errors.Join(result, fmt.Errorf("worker: %w", err))
			}
		case <-shutdownCtx.Done():
			result = errors.Join(result, fmt.Errorf("worker shutdown: %w", shutdownCtx.Err()))
			// Go не позволяет принудительно убить горутину. Зависимости воркера
			// обязаны учитывать контекст; пул нельзя закрывать до их завершения.
			if err := <-workerDone; err != nil {
				result = errors.Join(result, fmt.Errorf("worker: %w", err))
			}
		}
	}
	return result
}
