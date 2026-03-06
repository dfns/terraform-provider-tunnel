package libs

import (
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"

	ps "github.com/shirou/gopsutil/v4/process"
)

func WatchProcess(pid int) (err error) {
	parent, err := ps.NewProcess(int32(pid))
	if err != nil {
		return err
	}
	child, err := ps.NewProcess(int32(os.Getpid()))
	if err != nil {
		return err
	}
	// pool for parent process liveliness every 2 seconds
	go func() {
		for {
			_, err := parent.Status()
			if err != nil {
				log.Printf("parent process exited: %v\n", err)
				if runtime.GOOS == "windows" {
					err = child.Terminate()
				} else {
					err = child.SendSignal(syscall.SIGINT)
				}
				if err != nil {
					log.Printf("failed to terminate process: %v\n", err)
				}
			}
			time.Sleep(2 * time.Second)
		}
	}()

	return nil
}

func CheckProcessExists(pid int) error {
	cmd, err := ps.NewProcess(int32(pid))
	if err != nil {
		return err
	}
	stats, err := cmd.Status()
	if err != nil {
		return err
	}
	if stats[0] == "zombie" {
		return fmt.Errorf("process died")
	}

	return nil
}

func Interrupt(pid int) error {
	cmd, err := ps.NewProcess(int32(pid))
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		err = cmd.Terminate()
	} else {
		err = cmd.SendSignal(syscall.SIGINT)
	}
	if err != nil {
		return err
	}

	return nil
}

func WaitForPort(pid int, host string, port string) error {
	timeout := 30 * time.Second
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(host, port)
	for time.Now().Before(deadline) {
		if err := CheckProcessExists(pid); err != nil {
			return fmt.Errorf("process exited unexpectedly")
		}
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("port %s not accepting connections after %s", port, timeout)
}

func SignalReady(path string) error {
	return os.WriteFile(path, []byte("ready"), 0644)
}

func WaitForReadyFile(pid int, path string) error {
	timeout := 30 * time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := CheckProcessExists(pid); err != nil {
			return fmt.Errorf("process exited unexpectedly")
		}
		if _, err := os.Stat(path); err == nil {
			os.Remove(path)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("tunnel not ready after %s", timeout)
}
