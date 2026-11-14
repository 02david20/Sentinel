//go:build unix

package plugin

import (
	"os/exec"
	"syscall"
)

// newGroup starts the block in its own session: it has no controlling terminal (it cannot read
// the keyboard or answer a prompt) and its whole process group can be killed together.
func newGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
