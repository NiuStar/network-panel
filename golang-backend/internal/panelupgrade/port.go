package panelupgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ownsPort binds HTTP readiness to the systemd MainPID, not an unrelated server
// answering the same loopback address. Procfs symlinks are kernel-owned handles.
func ownsPort(pid int, port string) bool {
	number, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return false
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), fd.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	for _, family := range []string{"tcp", "tcp6"} {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, family))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			address, _, valid := strings.Cut(fields[1], ":")
			if !valid || (family == "tcp" && address != "00000000" && address != "0100007F") || (family == "tcp6" && address != "00000000000000000000000000000000") {
				continue
			}
			_, hexPort, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(hexPort, 16, 32)
			if err == nil && int(n) == number && inodes[fields[9]] {
				return true
			}
		}
	}
	return false
}
