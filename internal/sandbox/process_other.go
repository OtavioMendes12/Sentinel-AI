//go:build !unix

package sandbox

import "os/exec"

// configureProcessGroup is a no-op on platforms without process groups; the
// reviewer targets Linux CI runners.
func configureProcessGroup(*exec.Cmd) {}
