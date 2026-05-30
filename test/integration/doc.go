// Package integration contains integration tests for the tunnel provider's
// process-lifecycle primitives (the fork/watch machinery in internal/libs).
//
// This file is intentionally untagged so the package always has a buildable
// Go file, keeping `go build ./...` and linters happy when the `integration`
// build tag is absent. The tests themselves are guarded by that tag.
package integration
