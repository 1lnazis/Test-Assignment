package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type runnerFunc func(context.Context) error

func (f runnerFunc) Run(ctx context.Context) error { return f(ctx) }
func runtimeLogger() *slog.Logger                  { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func testListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
func receiveRuntime(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not stop")
		return nil
	}
}

func TestRunServices_CancellationWaitsForHTTPAndWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	workerStopped := make(chan struct{})
	var handlerStopped atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		handlerStopped.Store(true)
		_, _ = w.Write([]byte("done"))
	})}
	ln := testListener(t)
	background := runnerFunc(func(ctx context.Context) error { <-ctx.Done(); close(workerStopped); return nil })
	done := make(chan error, 1)
	go func() { done <- runServices(ctx, srv, ln, background, time.Second, runtimeLogger()) }()
	requestDone := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + ln.Addr().String())
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not started")
	}
	cancel()
	select {
	case <-workerStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not cancelled")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before handler finished: %v", err)
	default:
	}
	close(release)
	if err := receiveRuntime(t, done); err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if !handlerStopped.Load() {
		t.Fatal("handler was not joined")
	}
}

func TestRunServices_WorkerErrorStopsHTTP(t *testing.T) {
	want := errors.New("persist failed")
	ln := testListener(t)
	srv := &http.Server{Handler: http.NotFoundHandler()}
	err := runServices(context.Background(), srv, ln, runnerFunc(func(context.Context) error { return want }), time.Second, runtimeLogger())
	if !errors.Is(err, want) {
		t.Fatalf("run=%v", err)
	}
	if conn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("listener still open")
	}
}

func TestRunServices_ServerErrorCancelsAndJoinsWorker(t *testing.T) {
	ln := testListener(t)
	_ = ln.Close()
	var stopped atomic.Bool
	background := runnerFunc(func(ctx context.Context) error { <-ctx.Done(); stopped.Store(true); return nil })
	err := runServices(context.Background(), &http.Server{}, ln, background, time.Second, runtimeLogger())
	if err == nil || !stopped.Load() {
		t.Fatalf("err=%v stopped=%v", err, stopped.Load())
	}
}

func TestRunServices_HTTPDeadlineForcesConnectionClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, stopped := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(stopped) })}
	ln := testListener(t)
	done := make(chan error, 1)
	go func() {
		done <- runServices(ctx, srv, ln, runnerFunc(func(ctx context.Context) error { <-ctx.Done(); return nil }), time.Millisecond, runtimeLogger())
	}()
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + ln.Addr().String())
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not started")
	}
	cancel()
	if err := receiveRuntime(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run=%v", err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("handler context not cancelled")
	}
	<-requestDone
}
