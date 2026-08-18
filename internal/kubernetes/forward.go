package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type portForward struct {
	endpoint endpoint
	ready    <-chan struct{}
	stop     chan struct{}
	serve    func() error
}

func newPortForward(
	clientConfig *rest.Config,
	clientSet kubernetes.Interface,
	cfg TunnelConfig,
	ep endpoint,
) (*portForward, error) {
	transport, upgrader, err := spdy.RoundTripperFor(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("create round tripper: %w", err)
	}
	req := clientSet.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(cfg.Namespace).
		Name(ep.Pod).
		SubResource("portforward")
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())

	stopChan := make(chan struct{})
	readyChan := make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(
		dialer,
		[]string{cfg.LocalHost},
		[]string{fmt.Sprintf("%d:%d", cfg.LocalPort, ep.Port)},
		stopChan,
		readyChan,
		os.Stdout,
		os.Stderr,
	)
	if err != nil {
		return nil, fmt.Errorf("create port forwarder: %w", err)
	}
	return &portForward{
		endpoint: ep,
		ready:    readyChan,
		stop:     stopChan,
		serve:    forwarder.ForwardPorts,
	}, nil
}

func (f *portForward) run(ctx context.Context, signalReady func() error) error {
	defer close(f.stop)
	if ctx.Err() != nil {
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- f.serve() }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		return fmt.Errorf("start port forward to %s: %w", f.endpoint, endedError(err))
	case <-f.ready:
	}

	// Readiness can win the select when cancellation happens at the same time.
	if ctx.Err() != nil {
		return nil
	}

	log.Printf("forwarding to %s", f.endpoint)
	if err := signalReady(); err != nil {
		return err
	}
	log.Println("kubernetes tunnel is ready")

	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		return fmt.Errorf("port forward to %s ended: %w", f.endpoint, endedError(err))
	}
}

// endedError names the cause when client-go stops forwarding without reporting
// one, which would otherwise surface as an unexplained nil error.
func endedError(err error) error {
	if err == nil {
		return errors.New("client-go stopped forwarding")
	}
	return err
}
