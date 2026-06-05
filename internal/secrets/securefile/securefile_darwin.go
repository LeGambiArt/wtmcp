//go:build darwin

package securefile

import (
	"fmt"
	"os"
	"syscall"
)

// Create returns a new SecureFile backed by an unlinked temp file.
// The fd is inheritable by child processes. Use for shadow directory
// securefiles where plugin subprocesses need to read via /dev/fd/N.
//
// Go's runtime sets O_CLOEXEC on all fds by default, so we must
// explicitly clear FD_CLOEXEC to make the fd inheritable.
func Create(name string) (*SecureFile, error) {
	return createSecureFile(name, false)
}

// CreateCloexec returns a new SecureFile backed by an unlinked temp
// file with FD_CLOEXEC set. The fd is NOT inherited by child
// processes. Use for core-only consumers (TLS cert/key, OAuth2).
//
// Go's runtime already sets O_CLOEXEC, so the fcntl call is
// defensive (ensures the flag is set regardless of runtime changes).
func CreateCloexec(name string) (*SecureFile, error) {
	return createSecureFile(name, true)
}

func createSecureFile(name string, cloexec bool) (*SecureFile, error) {
	f, err := os.CreateTemp("", "wtmcp-sf-"+name+"-")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}

	path := f.Name()
	if err := os.Remove(path); err != nil {
		f.Close() //nolint:errcheck,gosec // best-effort cleanup on error
		return nil, fmt.Errorf("unlink temp file: %w", err)
	}

	fdNum := f.Fd()
	if err := setFdCloexec(fdNum, cloexec); err != nil {
		f.Close() //nolint:errcheck,gosec // best-effort cleanup on error
		return nil, fmt.Errorf("set fd flags: %w", err)
	}

	return &SecureFile{
		fd:   f,
		path: fmt.Sprintf("/dev/fd/%d", fdNum),
	}, nil
}

// setFdCloexec sets or clears the FD_CLOEXEC flag on a file
// descriptor.
func setFdCloexec(fd uintptr, cloexec bool) error {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFD, 0)
	if errno != 0 {
		return errno
	}

	var newFlags uintptr
	if cloexec {
		newFlags = flags | syscall.FD_CLOEXEC
	} else {
		newFlags = flags &^ syscall.FD_CLOEXEC
	}

	if newFlags != flags {
		_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFD, newFlags)
		if errno != 0 {
			return errno
		}
	}
	return nil
}
