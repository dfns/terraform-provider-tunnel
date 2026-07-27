package libs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// ConnServer serves local connections until shutdown.
type ConnServer struct {
	listener net.Listener
	handle   func(context.Context, net.Conn)

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

func NewConnServer(listener net.Listener, handle func(context.Context, net.Conn)) *ConnServer {
	return &ConnServer{listener: listener, handle: handle, conns: make(map[net.Conn]struct{})}
}

// Serve waits for active handlers before returning.
func (s *ConnServer) Serve(ctx context.Context) error {
	defer s.wg.Wait()
	stop := context.AfterFunc(ctx, s.Close)
	defer stop()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept local connection: %w", err)
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.run(ctx, conn)
	}
}

func (s *ConnServer) run(ctx context.Context, conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.forget(conn)
		s.wg.Done()
	}()
	s.handle(ctx, conn)
}

func (s *ConnServer) forget(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

// Close stops accepting and drops the connections in flight.
func (s *ConnServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	_ = s.listener.Close()
	for conn := range s.conns {
		_ = conn.Close()
	}
}
