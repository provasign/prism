package grove

import (
	"os"
	"os/exec"
	"testing"
)

func TestKillProcess(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Skip("can't spawn sleep")
	}
	pid := cmd.Process.Pid
	if err := killProcess(pid); err != nil {
		t.Errorf("kill: %v", err)
	}
	_ = cmd.Wait()
}

func TestKillProcess_InvalidPid(t *testing.T) {
	_ = killProcess(-99999)
}

func TestShutdown_WithPid(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Skip()
	}
	c := NewClient("http://x", "")
	c.startedPid = cmd.Process.Pid
	c.Shutdown()
	_ = cmd.Wait()
	// verify gone
	_, err := os.FindProcess(c.startedPid)
	_ = err
}
