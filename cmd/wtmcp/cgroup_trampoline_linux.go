package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// maybeCgroupTrampoline detects when the process cannot use cgroup
// resource limits (either because the cgroup isn't writable, or
// because controllers aren't delegated) and re-execs through
// systemd-run --user --scope --property=Delegate=yes so the process
// starts in a scope with delegated controllers.
//
// On success this function never returns (exec replaces the process).
// Returns nil if no trampoline is needed, or an error if the
// trampoline was needed but could not be performed.
func maybeCgroupTrampoline() error {
	// Guard against infinite re-exec.
	if os.Getenv("WTMCP_CGROUP_REEXEC") == "1" {
		return nil
	}

	cgPath, err := selfCgroupPath()
	if err != nil {
		log.Printf("cgroup trampoline: skipping, cannot read cgroup path: %v", err)
		return nil
	}

	if isWritable(cgPath) && hasControllers(cgPath) {
		return nil
	}

	log.Printf("cgroup trampoline: %s not usable by uid %d (writable=%v, controllers=%v), re-execing through systemd-run",
		cgPath, os.Getuid(), isWritable(cgPath), hasControllers(cgPath))

	return execTrampoline()
}

// selfCgroupPath reads /proc/self/cgroup and returns the filesystem
// path for the cgroups v2 unified hierarchy entry.
func selfCgroupPath() (string, error) {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // read-only file, nothing to recover

	return parseCgroupV2Path(f)
}

func parseCgroupV2Path(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if rel, ok := strings.CutPrefix(scanner.Text(), "0::"); ok {
			return "/sys/fs/cgroup" + rel, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading cgroup entries: %w", err)
	}
	return "", fmt.Errorf("no cgroups v2 entry")
}

// isWritable tests whether the current process can write to a path.
func isWritable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}

// hasControllers checks whether the cgroup has any controllers
// available for delegation (memory, pids, cpu).
func hasControllers(cgPath string) bool {
	data, err := os.ReadFile(filepath.Join(cgPath, "cgroup.controllers")) //nolint:gosec // cgPath comes from /proc/self/cgroup, not user input
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(data))) > 0
}

// execTrampoline re-execs the current process through systemd-run.
// Never returns on success.
func execTrampoline() error {
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return fmt.Errorf("systemd-run not found: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine own executable: %w", err)
	}

	uid := os.Getuid()

	// Ensure the user's systemd session bus is reachable.
	// su/sudo don't set these, but systemd-run --user needs them.
	xdgDir := os.Getenv("XDG_RUNTIME_DIR")
	if xdgDir == "" {
		xdgDir = fmt.Sprintf("/run/user/%d", uid)
		if info, err := os.Stat(xdgDir); err != nil || !info.IsDir() {
			return fmt.Errorf("XDG_RUNTIME_DIR not set and %s does not exist (user systemd session not active?)", xdgDir)
		}
		_ = os.Setenv("XDG_RUNTIME_DIR", xdgDir)
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		busPath := filepath.Join(xdgDir, "bus")
		if _, err := os.Stat(busPath); err == nil { //nolint:gosec // xdgDir is /run/user/<uid>, not user input
			_ = os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+busPath)
		}
	}

	// --scope creates a transient scope unit: the child inherits
	// the caller's environment, CWD, and stdio naturally (no relay).
	args := []string{
		"systemd-run",
		"--user", "--scope", "--collect", "--quiet",
		"-p", "KillMode=mixed",
		"-p", "Delegate=yes",
		"--",
		self,
	}
	args = append(args, os.Args[1:]...)

	_ = os.Setenv("WTMCP_CGROUP_REEXEC", "1")
	env := os.Environ()

	log.Printf("cgroup trampoline: exec %s", strings.Join(args, " ")) //nolint:gosec // args are constructed from constants + our own executable path

	return syscall.Exec(systemdRun, args, env) //nolint:gosec // systemdRun comes from LookPath, args from constants
}
