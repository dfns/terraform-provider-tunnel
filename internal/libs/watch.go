package libs

import (
	"fmt"
	"os"
	"strconv"
	"time"

	ps "github.com/shirou/gopsutil/v4/process"
)

func WatchProcess(pid string) (err error) {
	pidInt, err := strconv.Atoi(pid)
	if err != nil {
		return fmt.Errorf("invalid PID: %v", err)
	}
	parent, err := ps.NewProcess(int32(pidInt))
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
				fmt.Printf("parent process exited: %v\n", err)
				if err := child.Terminate(); err != nil {
					fmt.Printf("failed to terminate process: %v\n", err)
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
