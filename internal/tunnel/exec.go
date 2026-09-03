package tunnel

import (
	"fmt"
	"os/exec"
	"strings"
)

// run executes a command, folding its output into any error so failures are
// diagnosable — network tools report the real reason on stderr.
func run(name string, args ...string) error {
	_, err := output(name, args...)
	return err
}

// output executes a command and returns its combined output.
func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
