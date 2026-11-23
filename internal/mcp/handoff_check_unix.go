//go:build unix

package mcp

import (
	"fmt"
	"os"
	"syscall"
)

// handoffBinaryOK refuses to re-execute a binary another user could have
// written: owned by someone else, or group- or world-writable (SEC-08).
func handoffBinaryOK(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is group- or world-writable (%v); not re-executing it", path, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() && st.Uid != 0 {
		return fmt.Errorf("%s is owned by uid %d, not this user (%d); not re-executing it", path, st.Uid, os.Getuid())
	}
	return nil
}
