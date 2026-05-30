// Package sshtest provides an in-process SSH bastion for tunnel tests. The
// server authenticates a generated key and services "direct-tcpip" channels
// (the server side of `ssh -L`), so a tunnel can forward through it to an
// arbitrary local target without any external SSH daemon.
//
// The implementation lives in a build-tagged file (e2e || integration) so it is
// only compiled into test binaries that need it; this untagged file keeps the
// package buildable for `go build ./...` and linters when no tag is set.
package sshtest
