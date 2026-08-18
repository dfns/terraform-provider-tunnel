package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func readyChannel() <-chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}

var testEndpoint = endpoint{Pod: "web-1", Port: 8080}

func testPortForward(ready <-chan struct{}, run func(<-chan struct{}) error) *portForward {
	stopped := make(chan struct{})
	return &portForward{
		endpoint: testEndpoint,
		ready:    ready,
		stop:     stopped,
		serve:    func() error { return run(stopped) },
	}
}

func TestPortForwardReturnsErrorWhenSelectedPodEnds(t *testing.T) {
	want := errors.New("lost connection to pod")
	end := make(chan struct{})
	readyCalls := 0
	forward := testPortForward(readyChannel(), func(stopped <-chan struct{}) error {
		select {
		case <-end:
			return want
		case <-stopped:
			return nil
		}
	})

	err := forward.run(context.Background(), func() error {
		readyCalls++
		close(end)
		return nil
	})

	if !errors.Is(err, want) {
		t.Fatalf("run() error = %v, want %v", err, want)
	}
	if !strings.Contains(err.Error(), "web-1:8080") {
		t.Fatalf("run() error = %v, want selected endpoint", err)
	}
	if readyCalls != 1 {
		t.Fatalf("readiness signaled %d times, want once", readyCalls)
	}
}

func TestPortForwardFailsBeforeReadiness(t *testing.T) {
	forward := testPortForward(make(chan struct{}), func(<-chan struct{}) error { return nil })
	err := forward.run(context.Background(), func() error {
		t.Fatal("signaled readiness")
		return nil
	})

	if err == nil || !strings.Contains(err.Error(), "start port forward to web-1:8080") {
		t.Fatalf("run() error = %v, want pre-readiness failure", err)
	}
	if !strings.Contains(err.Error(), "client-go stopped forwarding") {
		t.Fatalf("run() error = %v, want the unreported-cause reason", err)
	}
}

func TestPortForwardReturnsSignalReadyErrorAndStops(t *testing.T) {
	want := errors.New("write ready file: permission denied")
	ended := make(chan struct{})
	forward := testPortForward(readyChannel(), func(stopped <-chan struct{}) error {
		<-stopped
		close(ended)
		return nil
	})

	err := forward.run(context.Background(), func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("run() error = %v, want %v", err, want)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("serve() left the port forward running")
	}
}

func TestPortForwardNeverAdvertisesACancelledTunnel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	forward := testPortForward(readyChannel(), func(stopped <-chan struct{}) error {
		<-stopped
		return nil
	})

	err := forward.run(ctx, func() error {
		t.Fatal("signaled readiness for a cancelled tunnel")
		return nil
	})
	if err != nil {
		t.Fatalf("run() error = %v, want nil on cancellation", err)
	}
}

func TestPortForwardStopsCleanlyOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	forward := testPortForward(readyChannel(), func(stopped <-chan struct{}) error {
		<-stopped
		close(ended)
		return nil
	})

	err := forward.run(ctx, func() error {
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("serve() did not stop the port forward")
	}
}
