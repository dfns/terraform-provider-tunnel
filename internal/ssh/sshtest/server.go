// Package sshtest provides an in-process SSH bastion for tunnel tests. The
// server authenticates a generated key and services the channel types a local
// forward opens ("direct-tcpip" and "direct-streamlocal@openssh.com", the server
// side of `ssh -L`), so a tunnel can forward through it to an arbitrary local
// target without any external SSH daemon.
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
// authenticates the given key and handles the channel types a local forward uses
// ("direct-tcpip" for `ssh -L`, "direct-streamlocal@openssh.com" for a unix
// socket target). It returns the listening port and tears the server down on test
// cleanup.
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
		switch nc.ChannelType() {
		case "direct-tcpip":
			go handleDirectTCPIP(nc)
		case "direct-streamlocal@openssh.com":
			go handleStreamLocal(nc)
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
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
	serveChannel(nc, "tcp", dest)
}

// handleStreamLocal is the unix-socket counterpart of handleDirectTCPIP (the
// server side of `ssh -L local:/path/to.sock`).
func handleStreamLocal(nc ssh.NewChannel) {
	var payload struct {
		SocketPath  string
		Reserved    string
		ReservedU32 uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &payload); err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "bad direct-streamlocal payload")
		return
	}
	serveChannel(nc, "unix", payload.SocketPath)
}

// serveChannel pipes the channel to its target, passing each direction's EOF on as a
// half-close the way a real sshd does.
//
// Deliberately its own copy loop rather than the production relay: the half-close
// tests measure the tunnel against this fixture, and sharing that code would put the
// same policy on both sides of the assertion, where a regression cancels out.
func serveChannel(nc ssh.NewChannel, network, address string) {
	target, err := net.Dial(network, address)
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

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(target, ch)
		closeWrite(target)
	}()
	_, _ = io.Copy(ch, target)
	_ = ch.CloseWrite()
	<-done

	_ = ch.Close()
	_ = target.Close()
}

// closeWrite mirrors libs.HalfClose, kept separate for the reason serveChannel gives.
func closeWrite(conn net.Conn) {
	if hc, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = hc.CloseWrite()
		return
	}
	_ = conn.Close()
}
