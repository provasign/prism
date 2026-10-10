package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/provasign/prism/internal/version"
)

// A stdio MCP client such as Claude Code does not respawn a server that exits,
// so retiring a superseded server leaves the session without Prism. Instead the
// old process starts the upgraded binary with the same arguments, replays the
// MCP handshake to it, and then only relays bytes: every request from here on
// is answered by the new code, and the client keeps its one connection.

// handoffArgs are the arguments the replacement server is started with.
var handoffArgs = func() []string { return os.Args[1:] }

const (
	handoffInitID      = "prism-handoff-initialize"
	handoffInitTimeout = 15 * time.Second
)

// startHandoff launches the replacement binary and completes its handshake.
// On success the returned reader yields the replacement's responses.
func (s *Server) startHandoff() (*exec.Cmd, io.WriteCloser, *bufio.Reader, error) {
	path := startupBinarySnapshot.path
	if path == "" {
		return nil, nil, nil, errors.New("launch path unknown")
	}
	cmd := exec.Command(path, handoffArgs()...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (*exec.Cmd, io.WriteCloser, *bufio.Reader, error) {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, nil, err
	}

	params := s.initParams
	if len(params) == 0 {
		params, _ = json.Marshal(map[string]any{
			"protocolVersion": defaultProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "prism-handoff", "version": version.Version},
		})
	}
	initReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": handoffInitID, "method": "initialize", "params": params,
	})
	if _, err := fmt.Fprintf(stdin, "%s\n", initReq); err != nil {
		return fail(err)
	}

	reader := bufio.NewReader(stdout)
	done := make(chan error, 1)
	go func() {
		msg, err := readMessage(reader)
		if err != nil {
			done <- err
			return
		}
		var resp struct {
			ID    any             `json:"id"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			done <- err
			return
		}
		if resp.ID != handoffInitID || len(resp.Error) > 0 {
			done <- fmt.Errorf("unexpected initialize reply: %s", msg)
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			return fail(err)
		}
	case <-time.After(handoffInitTimeout):
		return fail(errors.New("replacement did not answer initialize"))
	}
	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		return fail(err)
	}
	return cmd, stdin, reader, nil
}

// relayToReplacement forwards the pending request and the rest of the client
// stream to the replacement, and the replacement's output back to the client,
// until either side closes.
func relayToReplacement(cmd *exec.Cmd, childIn io.WriteCloser, childOut io.Reader, pending []byte, clientIn io.Reader, clientOut io.Writer) error {
	go func() {
		defer childIn.Close()
		if _, err := fmt.Fprintf(childIn, "%s\n", pending); err != nil {
			return
		}
		_, _ = io.Copy(childIn, clientIn)
	}()
	_, copyErr := io.Copy(clientOut, childOut)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return copyErr
	}
	if waitErr != nil {
		return fmt.Errorf("replacement server: %w", waitErr)
	}
	return nil
}
