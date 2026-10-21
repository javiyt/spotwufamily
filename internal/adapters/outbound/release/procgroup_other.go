//go:build !unix

package release

import "os/exec"

// killProcessGroup keeps exec.CommandContext's default: the script process
// itself is killed when the context ends.
func killProcessGroup(*exec.Cmd) {}
