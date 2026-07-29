package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dfns/terraform-provider-tunnel/internal/ssh/sshtest"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// isolateCredentials empties the ambient sources authMethods falls back to, so a
// test never picks up the developer's own keys or agent.
func isolateCredentials(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("SSH_AUTH_SOCK", "")
	return home
}

// generateKey returns a PEM-encoded ed25519 private key, encrypted when a
// passphrase is given.
func generateKey(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func writeKeyFile(t *testing.T, dir, name, keyPEM string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(keyPEM), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAuthMethodsAcceptsKeyInlineOrByPath covers the ssh_key contract inherited
// from the previous implementation: the attribute holds either PEM content or the
// path of a file holding it, and an existing file at that path is what decides.
func TestAuthMethodsAcceptsKeyInlineOrByPath(t *testing.T) {
	dir := isolateCredentials(t)
	plain := generateKey(t, "")
	encrypted := generateKey(t, "s3cret")

	tests := []struct {
		name string
		cfg  TunnelConfig
	}{
		{name: "inline key", cfg: TunnelConfig{SSHKey: plain}},
		{name: "key file", cfg: TunnelConfig{SSHKey: writeKeyFile(t, dir, "plain.pem", plain)}},
		{
			name: "inline encrypted key",
			cfg:  TunnelConfig{SSHKey: encrypted, SSHKeyPassphrase: "s3cret"},
		},
		{
			name: "encrypted key file",
			cfg: TunnelConfig{
				SSHKey:           writeKeyFile(t, dir, "encrypted.pem", encrypted),
				SSHKeyPassphrase: "s3cret",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			methods, err := authMethods(tt.cfg)
			if err != nil {
				t.Fatalf("authMethods() = %v", err)
			}
			if len(methods) != 1 {
				t.Fatalf("methods = %d, want 1 (public key)", len(methods))
			}
		})
	}
}

func TestAuthMethodsRejectsUnusableKey(t *testing.T) {
	isolateCredentials(t)
	tests := []struct {
		name    string
		cfg     TunnelConfig
		wantErr string
	}{
		{
			name:    "not a key",
			cfg:     TunnelConfig{SSHKey: "definitely not a PEM block"},
			wantErr: "parse SSH key",
		},
		{
			name:    "wrong passphrase",
			cfg:     TunnelConfig{SSHKey: generateKey(t, "s3cret"), SSHKeyPassphrase: "wrong"},
			wantErr: "parse encrypted SSH key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := authMethods(tt.cfg)
			if err == nil {
				t.Fatal("authMethods() = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestAuthMethodsOffersKeyAndPassword documents a deliberate widening: sshtun
// used whichever credential was set last, while a bastion may now be offered
// both.
func TestAuthMethodsOffersKeyAndPassword(t *testing.T) {
	isolateCredentials(t)
	methods, err := authMethods(TunnelConfig{SSHKey: generateKey(t, ""), SSHPassword: "hunter2"})
	if err != nil {
		t.Fatalf("authMethods() = %v", err)
	}
	if len(methods) != 2 {
		t.Fatalf("methods = %d, want 2 (public key, password)", len(methods))
	}
}

// TestAuthMethodsFallsBackToHomeDirectoryKeys keeps the implicit behaviour
// sshtun's automatic mode gave configurations that set no credentials at all.
func TestAuthMethodsFallsBackToHomeDirectoryKeys(t *testing.T) {
	home := isolateCredentials(t)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	writeKeyFile(t, filepath.Join(home, ".ssh"), "id_ed25519", generateKey(t, ""))

	methods, err := authMethods(TunnelConfig{})
	if err != nil {
		t.Fatalf("authMethods() = %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("methods = %d, want 1 (keys from ~/.ssh)", len(methods))
	}
}

func TestAuthMethodsWithoutAnyCredential(t *testing.T) {
	isolateCredentials(t)
	if _, err := authMethods(TunnelConfig{}); err == nil {
		t.Fatal("authMethods() = nil, want an error naming the ways to authenticate")
	}
}

// startAgent serves an in-process ssh-agent holding keyPEM and points
// SSH_AUTH_SOCK at it.
func startAgent(t *testing.T, keyPEM string) {
	t.Helper()
	key, err := ssh.ParseRawPrivateKey([]byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}

	// Not t.TempDir(): its path can exceed the unix socket path length limit.
	dir, err := os.MkdirTemp("", "agt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go acceptLoop(listener, func(conn net.Conn) { _ = agent.ServeAgent(keyring, conn) })
	t.Setenv("SSH_AUTH_SOCK", listener.Addr().String())
}

// TestAuthMethodsSignsWithAgentKey guards the agent path end to end: ssh asks the
// signers a PublicKeysCallback returned for a signature only after the callback
// has returned, so a signer bound to a connection the callback closed fails the
// handshake.
func TestAuthMethodsSignsWithAgentKey(t *testing.T) {
	isolateCredentials(t)
	keyPEM, authorizedKey := sshtest.GenerateClientKey(t)
	startAgent(t, keyPEM)
	sshAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(sshtest.StartServer(t, authorizedKey)))

	cfg, err := clientConfig(TunnelConfig{SSHUser: sshtest.User})
	if err != nil {
		t.Fatalf("clientConfig() = %v", err)
	}
	client, err := dialSSH(context.Background(), sshAddr, cfg)
	if err != nil {
		t.Fatalf("dialSSH() = %v", err)
	}
	_ = client.Close()
}

func TestClientConfigDefaultsToRoot(t *testing.T) {
	isolateCredentials(t)
	cfg, err := clientConfig(TunnelConfig{SSHKey: generateKey(t, "")})
	if err != nil {
		t.Fatalf("clientConfig() = %v", err)
	}
	if cfg.User != defaultSSHUser {
		t.Errorf("User = %q, want %q", cfg.User, defaultSSHUser)
	}
	if cfg.Timeout != dialTimeout {
		t.Errorf("Timeout = %v, want %v", cfg.Timeout, dialTimeout)
	}
	if cfg.HostKeyCallback == nil {
		t.Error("HostKeyCallback is nil, which makes every handshake fail")
	}
}
