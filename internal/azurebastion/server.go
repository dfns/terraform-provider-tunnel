package azurebastion

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/dfns/terraform-provider-tunnel/internal/libs"
)

type server struct {
	session *sessionClient
}

func newServer(listener net.Listener, session *sessionClient) *libs.ConnServer {
	s := &server{session: session}
	return libs.NewConnServer(listener, s.handle)
}

func (s *server) handle(ctx context.Context, local net.Conn) {
	remote, err := s.session.open(ctx)
	if err != nil {
		log.Printf("Azure Bastion connection failed: %v", err)
		return
	}
	relay(local, remote)
}

// Prevent an unresponsive local client from pinning session shutdown.
const drainGrace = 5 * time.Second

func relay(local net.Conn, remote webSocketConn) {
	libs.RelayDrain(local, &wsStream{conn: remote}, drainGrace)
}
