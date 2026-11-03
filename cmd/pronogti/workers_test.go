package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackgroundWorkersStopCancelsAllJobsAndWaitsForPersistence(t *testing.T) {
	w := newBackgroundWorkers()
	started := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	for range 2 {
		w.Start("test", func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			finished <- struct{}{}
			return ctx.Err()
		})
	}
	<-started
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if len(finished) != 2 {
		t.Fatal("shutdown returned before all workers finished")
	}
}

func TestBackgroundWorkersShutdownIsBounded(t *testing.T) {
	w := newBackgroundWorkers()
	release := make(chan struct{})
	w.Start("blocked", func(context.Context) error { <-release; return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected stop result: %v", err)
	}
	close(release)
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
