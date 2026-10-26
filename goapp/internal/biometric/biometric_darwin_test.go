package biometric

import (
	"errors"
	"testing"
	"time"
)

// Exercises the block callback path without a human: the request is
// invalidated right after it starts, so the reply arrives (from a
// foreign thread) with a cancel error instead of hanging.
func TestEvaluateReplyArrivesAndMapsCancel(t *testing.T) {
	if !Available() {
		t.Skip("no device-owner authentication on this Mac")
	}
	ctx, err := newContext()
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Send(selRelease)
	out := make(chan error, 1)
	go func() {
		out <- evaluate(ctx, "test de tuissh", func() {
			time.Sleep(300 * time.Millisecond)
			ctx.Send(selInval)
		})
	}()
	select {
	case err := <-out:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("want ErrCancelled, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reply never arrived: block callback is broken")
	}
}

func TestAvailabilityDoesNotPanic(t *testing.T) {
	t.Logf("biometrics=%v available=%v", Biometrics(), Available())
}
