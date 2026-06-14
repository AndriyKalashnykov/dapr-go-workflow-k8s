package main

import (
	"context"
	"testing"
)

func TestRegisterShutdownCancelsServices(t *testing.T) {
	var aCancelled, bCancelled bool
	services := map[string]context.CancelFunc{
		"a": func() { aCancelled = true },
		"b": func() { bCancelled = true },
	}

	ctx, cancel := registerShutdown(context.Background(), services)
	if ctx.Err() != nil {
		t.Fatal("context should not be cancelled before cancel() is called")
	}

	cancel()

	if !aCancelled || !bCancelled {
		t.Errorf("expected all service cancels to run: a=%v b=%v", aCancelled, bCancelled)
	}
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled after cancel()")
	}
}
