package ssh

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	defaultSSHPort = 22
	defaultSSHUser = "root"
	dialTimeout    = 15 * time.Second
)

// Used when no credentials are configured.
var defaultKeyNames = []string{"id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk"}

func clientConfig(cfg TunnelConfig) (*ssh.ClientConfig, error) {
	methods, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	sshUser := cfg.SSHUser
	if sshUser == "" {
		sshUser = defaultSSHUser
	}
	return &ssh.ClientConfig{
		User: sshUser,
		Auth: methods,
		// Preserve existing behavior for newly-created bastions with unknown keys.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         dialTimeout,
	}, nil
}

func authMethods(cfg TunnelConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if cfg.SSHKey != "" {
		signer, err := parseConfiguredKey(cfg.SSHKey, cfg.SSHKeyPassphrase)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if cfg.SSHPassword != "" {
		methods = append(methods, ssh.Password(cfg.SSHPassword))
	}
	if len(methods) > 0 {
		return methods, nil
	}

	if keys := defaultKeys(); keys != nil {
		methods = append(methods, keys)
	}
	if keys := agentKeys(); keys != nil {
		methods = append(methods, keys)
	}
	if len(methods) == 0 {
		return nil, errors.New("no SSH credentials: set ssh_key or ssh_password, " +
			"keep a key in ~/.ssh, or run an ssh-agent")
	}
	return methods, nil
}

// ssh_key accepts either inline PEM or a file path.
func parseConfiguredKey(key, passphrase string) (ssh.Signer, error) {
	pemBytes := []byte(key)
	if _, statErr := os.Stat(key); statErr == nil {
		read, err := os.ReadFile(key)
		if err != nil {
			return nil, fmt.Errorf("read SSH key file %s: %w", key, err)
		}
		pemBytes = read
	}
	if passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("parse encrypted SSH key: %w", err)
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key: %w", err)
	}
	return signer, nil
}

// Ignore unusable default keys because they are implicit candidates.
func defaultKeys() ssh.AuthMethod {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	var signers []ssh.Signer
	for _, name := range defaultKeyNames {
		pemBytes, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(pemBytes)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}
	if len(signers) == 0 {
		return nil
	}
	return ssh.PublicKeys(signers...)
}

// Probe now, but reconnect for each callback to avoid holding the agent socket.
func agentKeys() ssh.AuthMethod {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil
	}
	probe, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}
	_ = probe.Close()

	return ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		return agent.NewClient(conn).Signers()
	})
}
