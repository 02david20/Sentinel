//go:build !windows

package blocks

import "syscall"

// detach puts the agent session in its own process group so it outlives the runner.
func detach() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
