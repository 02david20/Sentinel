//go:build unix

package plugin

import (
	"os/exec"
	"syscall"
)

// newGroup runs the block in its own process group so a timeout can stop its children too.
func newGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
