//go:build e2e || integration

package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
)

// User is the username the in-process server accepts. Authentication is by key,
// so the username is only there to satisfy the SSH handshake.
const User = "tunneltest"

// newEd25519Signer generates a fresh ed25519 key pair and returns the raw
// private key (for PEM marshaling) alongside its SSH signer.
func newEd25519Signer(t testing.TB) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ed25519 key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("creating signer: %v", err)
	}
	return priv, signer
}

// GenerateClientKey returns an OpenSSH-PEM-encoded private key (suitable for the
// provider's ssh_key) and the matching public key to authorize on the server.
func GenerateClientKey(t testing.TB) (string, ssh.PublicKey) {
	t.Helper()
	priv, signer := newEd25519Signer(t)
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), signer.PublicKey()
}

// StartServer starts a minimal SSH server on a random localhost port that
// authenticates the given key and handles "direct-tcpip" (ssh -L) channels so a
// local forward works. It returns the listening port and tears the server down
// on test cleanup.
func StartServer(t testing.TB, authorizedKey ssh.PublicKey) int {
	t.Helper()

	_, hostSigner := newEd25519Signer(t)
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorizedKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized public key")
		},
	}
	config.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed during cleanup
			}
			go handleSSHConn(conn, config)
		}
	}()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is not TCP: %T", ln.Addr())
	}
	return addr.Port
}

func handleSSHConn(c net.Conn, config *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(c, config)
	if err != nil {
		return // handshake/auth failure
	}
	defer sshConn.Close()

	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			_ = nc.Reject(ssh.UnknownChannelType, "only direct-tcpip is supported")
			continue
		}
		go handleDirectTCPIP(nc)
	}
}

// handleDirectTCPIP dials the requested destination and pipes bytes between the
// SSH channel and that connection (the server side of `ssh -L`).
func handleDirectTCPIP(nc ssh.NewChannel) {
	var payload struct {
		DestAddr string
		DestPort uint32
		SrcAddr  string
		SrcPort  uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &payload); err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
		return
	}

	dest := net.JoinHostPort(payload.DestAddr, strconv.Itoa(int(payload.DestPort)))
	target, err := net.Dial("tcp", dest)
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}

	ch, chReqs, err := nc.Accept()
	if err != nil {
		_ = target.Close()
		return
	}
	go ssh.DiscardRequests(chReqs)

	go func() {
		_, _ = io.Copy(ch, target)
		_ = ch.CloseWrite()
	}()
	go func() {
		_, _ = io.Copy(target, ch)
		_ = target.Close()
	}()
}
