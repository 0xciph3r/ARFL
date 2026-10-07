//go:build !darwin && !linux

package helper

import (
	"errors"
	"os/exec"
)

// ConfigPath is unused where the helper is unsupported.
const ConfigPath = ""

var errUnsupported = errors.New("the ARFL helper is not supported on this platform yet; run ARFL as administrator")

func Install(string, int) error { return errUnsupported }

func Uninstall() error { return errUnsupported }

func ElevatedCommand(args []string) *exec.Cmd { return exec.Command(args[0], args[1:]...) }
