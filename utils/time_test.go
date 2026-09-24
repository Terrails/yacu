package utils

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSleepReturnsEarlyWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("sleep did not return early")
	}
}

func TestSleepWaits(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
