package cli

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// pickPort returns a port to listen on. If defaultPort is free it is returned
// immediately. Otherwise up to 5 alternatives in [defaultPort+1,
// defaultPort+20] are listed on stderr and the user is prompted to select one.
// When stdin is not a terminal the first available alternative is used without
// prompting.
func pickPort(defaultPort int) (int, error) {
	if defaultPort < 1 || defaultPort > 65535 {
		return 0, fmt.Errorf("invalid port %d (must be 1–65535)", defaultPort)
	}
	if isPortFree(defaultPort) {
		return defaultPort, nil
	}

	const (
		scanRange = 20
		maxOpts   = 5
	)
	var candidates []int
	for p := defaultPort + 1; p <= defaultPort+scanRange && len(candidates) < maxOpts; p++ {
		if p > 65535 {
			break
		}
		if isPortFree(p) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return 0, fmt.Errorf("port %d is already in use; no alternatives found in %d–%d",
			defaultPort, defaultPort+1, defaultPort+scanRange)
	}

	fmt.Fprintf(os.Stderr, "\nport %d is already in use. Available alternatives:\n", defaultPort)
	for i, p := range candidates {
		fmt.Fprintf(os.Stderr, "  [%d] %d\n", i+1, p)
	}

	if !stdinIsTerminal() {
		fmt.Fprintf(os.Stderr, "stdin is not a terminal; using port %d\n", candidates[0])
		return candidates[0], nil
	}

	fmt.Fprintf(os.Stderr, "Select [1–%d] (default 1): ", len(candidates))
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return candidates[0], nil
	}
	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(candidates) {
		return 0, fmt.Errorf("invalid selection %q", line)
	}
	return candidates[idx-1], nil
}

// isPortFree reports whether the given TCP port is available on localhost.
func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// stdinIsTerminal reports whether os.Stdin is connected to an interactive terminal.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
