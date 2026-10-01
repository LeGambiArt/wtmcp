package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// newCleanup starts a goroutine mirroring run()'s cleanup goroutine: it blocks
// on ctx.Done() and then closes the returned channel. serveAndWait must cancel
// the context so this goroutine can finish, otherwise the receive on the
// channel blocks forever.
func newCleanup(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
	}()
	return done
}

// TestServeAndWaitEarlyError reproduces the startup-hang: serve returns an
// error before any shutdown signal, so nothing has cancelled the context. Prior
// to the fix, serveAndWait blocked forever on cleanupDone; now it cancels the
// context itself and returns the error promptly.
func TestServeAndWaitEarlyError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanupDone := newCleanup(ctx)

	wantErr := errors.New("server TLS: invalid CA")
	got := make(chan error, 1)
	go func() {
		got <- serveAndWait(cancel, cleanupDone, func() error { return wantErr })
	}()

	select {
	case err := <-got:
		if !errors.Is(err, wantErr) {
			t.Fatalf("serveAndWait err = %v, want %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveAndWait hung after serve returned an error; cleanup was not triggered")
	}
}

// TestServeAndWaitSignalShutdown covers the normal path: an external signal
// cancels the context, serve unblocks and returns nil, and serveAndWait
// completes cleanly (the redundant stop() is harmless).
func TestServeAndWaitSignalShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanupDone := newCleanup(ctx)

	got := make(chan error, 1)
	go func() {
		got <- serveAndWait(cancel, cleanupDone, func() error {
			<-ctx.Done() // serve blocks until shutdown, like a live transport
			return nil
		})
	}()

	// Simulate a SIGINT/SIGTERM arriving.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("serveAndWait err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveAndWait hung on the normal shutdown path")
	}
}
