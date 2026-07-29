package ssh

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Keep the connection ahead of Azure's idle timeout.
const keepaliveInterval = 30 * time.Second

// dialSSH uses one context for the TCP connection and SSH handshake.
func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	connectCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		connectCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(connectCtx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	// NewClientConn has no context support; closing the transport unblocks it.
	closeDone := make(chan struct{})
	stopClose := context.AfterFunc(connectCtx, func() {
		_ = conn.Close()
		close(closeDone)
	})
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if !stopClose() {
		<-closeDone
	}
	if ctxErr := connectCtx.Err(); ctxErr != nil {
		_ = conn.Close()
		return nil, ctxErr
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// clientPool multiplexes forwarded channels over one SSH connection.
type clientPool struct {
	dial func(context.Context) (*ssh.Client, error)

	mu       sync.Mutex
	client   *ssh.Client
	inflight *handshake
	closed   bool
}

type handshake struct {
	done   chan struct{}
	client *ssh.Client
	err    error
}

// Concurrent callers share one in-flight handshake.
func (p *clientPool) get(ctx context.Context) (*ssh.Client, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, net.ErrClosed
	}
	if p.client != nil {
		client := p.client
		p.mu.Unlock()
		return client, nil
	}
	if attempt := p.inflight; attempt != nil {
		p.mu.Unlock()
		select {
		case <-attempt.done:
			return attempt.client, attempt.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	attempt := &handshake{done: make(chan struct{})}
	p.inflight = attempt
	p.mu.Unlock()

	client, err := p.dial(ctx)

	p.mu.Lock()
	p.inflight = nil
	if err == nil {
		if p.closed {
			_ = client.Close()
			client, err = nil, net.ErrClosed
		} else {
			p.client = client
			go p.tend(client)
		}
	}
	p.mu.Unlock()

	attempt.client, attempt.err = client, err
	close(attempt.done)
	return client, err
}

func (p *clientPool) tend(client *ssh.Client) {
	go keepalive(client)
	log.Printf("ssh connection ended: %v", client.Wait())
	p.discard(client)
}

func keepalive(client *ssh.Client) {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for range ticker.C {
		if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			return
		}
	}
}

func (p *clientPool) discard(client *ssh.Client) {
	p.mu.Lock()
	if p.client == client {
		p.client = nil
	}
	p.mu.Unlock()
	_ = client.Close()
}

func (p *clientPool) close() {
	p.mu.Lock()
	client := p.client
	p.client, p.closed = nil, true
	p.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}
