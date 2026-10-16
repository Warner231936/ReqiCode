package config

import (
	"net"
	"os/exec"
	"strconv"
	"time"
)

// portOpen reports whether something is listening on a local port.
//
// A short TCP connect is used rather than an HTTP request because a server may
// be mid-startup and answer a probe with a protocol error, which is still
// evidence that it exists. Treating "wrong response" as "absent" would hide a
// running server and make discovery non-deterministic.
func portOpen(host string, port int) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// exeLookPath is os/exec.LookPath with a Windows-safe extension list.
//
// exec.LookPath on Windows consults PATHEXT, which normally covers .exe. Wrapping
// it keeps the intent explicit and gives one place to add a name if the vendored
// binary is ever renamed.
func exeLookPath(name string) (string, error) {
	return exec.LookPath(name)
}