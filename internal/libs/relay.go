package libs

import (
	"io"
	"time"
)

type halfCloser interface {
	CloseWrite() error
}

// HalfClose signals EOF without closing the read side.
func HalfClose(stream io.Closer) bool {
	hc, ok := stream.(halfCloser)
	if !ok {
		return false
	}
	return hc.CloseWrite() == nil
}

// Relay copies both directions and preserves half-close semantics.
func Relay(a, b io.ReadWriteCloser) {
	RelayDrain(a, b, 0)
}

// RelayDrain bounds how long a may keep sending after b reaches EOF.
// A zero grace period waits indefinitely.
func RelayDrain(a, b io.ReadWriteCloser, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		pipe(b, a)
	}()
	pipe(a, b)

	if grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = a.Close()
			<-done
		}
	} else {
		<-done
	}

	_ = a.Close()
	_ = b.Close()
}

func pipe(dst, src io.ReadWriteCloser) {
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = src.Close()
		return
	}
	if !HalfClose(dst) {
		_ = dst.Close()
	}
}
