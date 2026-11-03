package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// backgroundWorkers own application jobs independently of browser requests.
// Shutdown cancels every worker before waiting, so the individual workers can
// persist an uncertain in-flight outcome within their bounded cleanup budgets.
type backgroundWorkers struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newBackgroundWorkers() *backgroundWorkers {
	ctx, cancel := context.WithCancel(context.Background())
	return &backgroundWorkers{ctx: ctx, cancel: cancel}
}

func (w *backgroundWorkers) Start(name string, run func(context.Context) error) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		if err := run(w.ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("background worker stopped", "worker", name, "error", err)
		}
	}()
}

func (w *backgroundWorkers) Stop(ctx context.Context) error {
	w.cancel()
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
