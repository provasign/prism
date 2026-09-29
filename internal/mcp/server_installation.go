package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/provasign/prism/internal/version"
)

type prismInstallation struct {
	path    string
	version string
}

var (
	installationCheckOnce sync.Once
	installationNoteMu    sync.Mutex
	installationNote      string
	installationNoteShown bool
)

// conflictingInstallationNote warns once when an MCP server pinned to one
// Prism executable can see another installation with a different release.
// This catches a common upgrade trap: the installer and Homebrew can coexist,
// while agent configuration continues launching the older absolute path.
func conflictingInstallationNote() string {
	installationCheckOnce.Do(func() {
		running, err := os.Executable()
		if err != nil {
			return
		}
		installationNote = PrismInstallationWarning(running, version.Version)
	})

	installationNoteMu.Lock()
	defer installationNoteMu.Unlock()
	if installationNote == "" || installationNoteShown {
		return ""
	}
	installationNoteShown = true
	return installationNote
}

// PrismInstallationWarning returns a user-facing diagnostic when distinct
// Prism executables on this machine report different release versions. Init
// uses it before writing MCP registrations; the server uses it after startup
// to catch configurations that still pin an older executable.
func PrismInstallationWarning(runningPath, runningVersion string) string {
	if normalizePrismVersion(runningVersion) == "" || runningVersion == "dev" {
		return ""
	}
	home, _ := os.UserHomeDir()
	conflicts := findPrismInstallationConflicts(
		runningPath, runningVersion, os.Getenv("PATH"), home, runtime.GOOS, probePrismVersion,
	)
	if len(conflicts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(conflicts))
	for _, conflict := range conflicts {
		parts = append(parts, fmt.Sprintf("%s (%s)", conflict.path, conflict.version))
	}
	return "⚠ multiple Prism installations have different versions. This process is running " +
		runningVersion + " from " + runningPath + "; also found " + strings.Join(parts, ", ") + ". " +
		"MCP clients pin executable paths, so updating one installation may leave agents on another. " +
		"Run the intended Prism executable with `cleanup-global`, run `prism init` in each affected project, then restart the agent."
}

func findPrismInstallationConflicts(
	runningPath, runningVersion, pathValue, home, goos string,
	probe func(string) (string, error),
) []prismInstallation {
	runningVersion = normalizePrismVersion(runningVersion)
	if runningVersion == "" {
		return nil
	}
	runningInfo, _ := os.Stat(runningPath)
	seen := map[string]bool{}
	var conflicts []prismInstallation
	for _, candidate := range prismInstallationCandidates(pathValue, home, goos) {
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		canonical, err = filepath.Abs(canonical)
		if err != nil || seen[canonical] {
			continue
		}
		seen[canonical] = true
		info, err := os.Stat(canonical)
		if err != nil || info.IsDir() || (runningInfo != nil && os.SameFile(runningInfo, info)) {
			continue
		}
		foundVersion, err := probe(canonical)
		if err != nil {
			continue
		}
		foundVersion = normalizePrismVersion(foundVersion)
		if foundVersion != "" && foundVersion != runningVersion {
			conflicts = append(conflicts, prismInstallation{path: candidate, version: "v" + foundVersion})
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].path < conflicts[j].path })
	return conflicts
}

func prismInstallationCandidates(pathValue, home, goos string) []string {
	name := "prism"
	if goos == "windows" {
		name = "prism.exe"
	}
	dirs := filepath.SplitList(pathValue)
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "bin"))
	}
	if goos != "windows" {
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	}
	paths := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if !seen[candidate] {
			seen[candidate] = true
			paths = append(paths, candidate)
		}
	}
	return paths
}

func probePrismVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func normalizePrismVersion(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	value = fields[len(fields)-1]
	value = strings.TrimPrefix(value, "v")
	if value == "" || value == "dev" {
		return ""
	}
	return value
}
