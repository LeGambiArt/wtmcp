// Package pinentry provides a client for the GnuPG pinentry program,
// which is used to securely prompt users for passwords via the Assuan
// line protocol. This avoids storing passwords in files or environment
// variables.
package pinentry

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ErrCancelled is returned when the user cancels the pinentry dialog.
var ErrCancelled = errors.New("pinentry: operation cancelled by user")

// Client communicates with a pinentry process using the Assuan protocol.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
}

// New creates a pinentry client by locating and launching a pinentry
// binary. The search order is:
//  1. $WTMCP_PINENTRY environment variable
//  2. Platform-specific binaries (pinentry-mac, pinentry-gnome3, etc.)
//  3. Generic "pinentry" binary
//
// Returns an error if no pinentry binary is found or the process fails
// to start.
func New() (*Client, error) {
	path, err := findPinentry()
	if err != nil {
		return nil, err
	}
	return newFromPath(path)
}

// Available reports whether a pinentry binary can be found on the
// system. Does not launch a process.
func Available() bool {
	_, err := findPinentry()
	return err == nil
}

// DefaultTimeout is the maximum time GetPassword will wait for the
// user to enter a password. Callers can override via GetPasswordContext.
const DefaultTimeout = 5 * time.Minute

// GetPassword prompts the user for a password using DefaultTimeout.
// The prompt is the short label shown next to the input field. The
// description provides additional context.
func (c *Client) GetPassword(prompt, description string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	return c.GetPasswordContext(ctx, prompt, description)
}

// GetPasswordContext prompts the user for a password. If ctx is
// cancelled or its deadline is exceeded before the user responds, the
// pinentry process is killed and an error is returned.
func (c *Client) GetPasswordContext(ctx context.Context, prompt, description string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if description != "" {
		if err := c.command("SETDESC " + encode(description)); err != nil {
			return nil, fmt.Errorf("SETDESC: %w", err)
		}
	}

	if prompt != "" {
		if err := c.command("SETPROMPT " + encode(prompt)); err != nil {
			return nil, fmt.Errorf("SETPROMPT: %w", err)
		}
	}

	type result struct {
		password []byte
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		p, err := c.getpin()
		ch <- result{p, err}
	}()

	select {
	case r := <-ch:
		return r.password, r.err
	case <-ctx.Done():
		_ = c.cmd.Process.Kill()
		return nil, fmt.Errorf("pinentry: %w", ctx.Err())
	}
}

// Close terminates the pinentry process. It sends BYE, closes stdin,
// then waits for the process to exit with a short timeout.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_ = c.sendLine("BYE")
	_ = c.stdin.Close()

	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		return fmt.Errorf("pinentry: process did not exit within 5 seconds")
	}
}

func newFromPath(path string) (*Client, error) {
	cmd := exec.Command(path) //nolint:gosec // path from findPinentry
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pinentry stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("pinentry stdout: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start pinentry: %w", err)
	}

	c := &Client{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}

	// Read the initial OK greeting
	if err := c.expectOK(); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("pinentry greeting: %w", err)
	}

	return c, nil
}

// command sends a command and expects an OK response.
func (c *Client) command(cmd string) error {
	if err := c.sendLine(cmd); err != nil {
		return err
	}
	return c.expectOK()
}

// getpin sends GETPIN and reads the D response line containing the
// password.
func (c *Client) getpin() ([]byte, error) {
	if err := c.sendLine("GETPIN"); err != nil {
		return nil, err
	}

	for {
		line, err := c.readLine()
		if err != nil {
			return nil, fmt.Errorf("read GETPIN response: %w", err)
		}

		switch {
		case strings.HasPrefix(line, "D "):
			password := []byte(decode(line[2:]))
			// Still need to read the trailing OK
			if err := c.expectOK(); err != nil {
				zeroBytes(password)
				return nil, err
			}
			return password, nil
		case strings.HasPrefix(line, "OK"):
			// Empty password
			return []byte{}, nil
		case strings.HasPrefix(line, "ERR "):
			return nil, parsePinentryError(line)
		default:
			// Skip comment lines (# ...) and S lines
			continue
		}
	}
}

func (c *Client) sendLine(line string) error {
	_, err := fmt.Fprintf(c.stdin, "%s\n", line)
	return err
}

func (c *Client) readLine() (string, error) {
	line, err := c.stdout.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *Client) expectOK() error {
	line, err := c.readLine()
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if strings.HasPrefix(line, "OK") {
		return nil
	}
	if strings.HasPrefix(line, "ERR ") {
		return parsePinentryError(line)
	}
	return fmt.Errorf("unexpected response: %q", line)
}

func parsePinentryError(line string) error {
	// ERR <code> <description>
	parts := strings.SplitN(line, " ", 3)
	// GPG_ERR_CANCELED (83886179) indicates the user dismissed the dialog.
	if len(parts) >= 2 && parts[1] == "83886179" {
		return ErrCancelled
	}
	if len(parts) >= 3 {
		return fmt.Errorf("pinentry error: %s", parts[2])
	}
	return fmt.Errorf("pinentry error: %s", line)
}

// Assuan percent-encoding: %XX for special characters.
func encode(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch c {
		case '%':
			b.WriteString("%25")
		case '\n':
			b.WriteString("%0A")
		case '\r':
			b.WriteString("%0D")
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func decode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi := unhex(s[i+1])
			lo := unhex(s[i+2])
			if hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo)) //nolint:gosec // hi,lo are 0-15
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c - 'a' + 10)
	case c >= 'A' && c <= 'F':
		return int(c - 'A' + 10)
	default:
		return -1
	}
}

func findPinentry() (string, error) {
	if env := os.Getenv("WTMCP_PINENTRY"); env != "" {
		if path, err := exec.LookPath(env); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("WTMCP_PINENTRY=%q not found", env)
	}

	candidates := pinentrySearchOrder()
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no pinentry binary found (tried %s); set WTMCP_PINENTRY to specify one",
		strings.Join(candidates, ", "))
}

func pinentrySearchOrder() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"pinentry-mac", "pinentry-tty", "pinentry-curses", "pinentry"}
	case "linux":
		return []string{"pinentry-gnome3", "pinentry-qt", "pinentry-curses", "pinentry-tty", "pinentry"}
	default:
		return []string{"pinentry-curses", "pinentry-tty", "pinentry"}
	}
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
