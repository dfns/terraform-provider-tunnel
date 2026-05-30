package libs

import (
	"os"
	"path/filepath"
)

func TunnelLogPath(name string) string {
	dir := os.Getenv(TunnelLogDirEnv)
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, name)
}
