package rex

import (
	"fmt"
	"os"
	"strings"
)

// inspect returns the name, arguments and working directory of a process.
func inspect(pid int) (p procInfo) {
	if pid <= 0 {
		return
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		p.name = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		p.args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	}
	p.dir, _ = os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	return p
}

func sysctlString(string) string { return "" }

func sysctlUint64(string) uint64 { return 0 }
