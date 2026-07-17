package clientcore

import (
	"context"
	"testing"
	"time"
)

func TestSleepOrDoneReturnsEarlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	sleepOrDone(ctx, time.Hour)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("sleepOrDone did not return promptly on a cancelled context: waited %s", elapsed)
	}
}

func TestSleepOrDoneWaitsWhenNotCancelled(t *testing.T) {
	start := time.Now()
	sleepOrDone(context.Background(), 20*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("sleepOrDone returned before the timer elapsed: waited %s", elapsed)
	}
}
