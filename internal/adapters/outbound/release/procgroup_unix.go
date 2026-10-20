//go:build unix

package release

import (
	"os/exec"
	"syscall"
)

// killProcessGroup starts the script in its own process group and kills the
// whole group (the script and any gh/go it started) when the context ends.
func killProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
