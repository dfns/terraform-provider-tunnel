package ssh

import (
	"context"
	"errors"
	"log"
	"net"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
	"golang.org/x/crypto/ssh"
)

type forwarder struct {
	clients *clientPool
	network string
	target  string
}

func newForwarder(listener net.Listener, clients *clientPool, network, target string) *libs.ConnServer {
	f := &forwarder{clients: clients, network: network, target: target}
	return libs.NewConnServer(listener, f.handle)
}

func (f *forwarder) handle(ctx context.Context, local net.Conn) {
	remote, err := f.dialTarget(ctx)
	if err != nil {
		// A target may come up later without requiring a new tunnel.
		log.Printf("forward to %s failed: %v", f.target, err)
		return
	}
	libs.Relay(local, remote)
}

// Retry transport failures once; target rejections leave the SSH client usable.
func (f *forwarder) dialTarget(ctx context.Context) (net.Conn, error) {
	var lastErr error
	for range 2 {
		client, err := f.clients.get(ctx)
		if err != nil {
			return nil, err
		}
		remote, err := client.Dial(f.network, f.target)
		if err == nil {
			return remote, nil
		}
		var openErr *ssh.OpenChannelError
		if errors.As(err, &openErr) {
			return nil, err
		}
		f.clients.discard(client)
		lastErr = err
	}
	return nil, lastErr
}
