package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func TestParseCgroupV2Path(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "v2 unified hierarchy",
			input: "0::/user.slice/user-1000.slice/session-2.scope\n",
			want:  "/sys/fs/cgroup/user.slice/user-1000.slice/session-2.scope",
		},
		{
			name:  "v2 root cgroup",
			input: "0::/\n",
			want:  "/sys/fs/cgroup/",
		},
		{
			name:  "v2 entry after v1 entries",
			input: "1:cpu:/system\n2:memory:/system\n0::/user.slice\n",
			want:  "/sys/fs/cgroup/user.slice",
		},
		{
			name:    "v1 only",
			input:   "1:cpu:/\n2:memory:/\n",
			wantErr: true,
		},
		{
			name:    "empty file",
			input:   "",
			wantErr: true,
		},
		{
			name:  "v2 found before trailing v1",
			input: "0::/early\n1:cpu:/late\n",
			want:  "/sys/fs/cgroup/early",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCgroupV2Path(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCgroupV2Path() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseCgroupV2Path() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("io error surfaces instead of misleading message", func(t *testing.T) {
		ioErr := errors.New("simulated read error")
		_, err := parseCgroupV2Path(iotest.ErrReader(ioErr))
		if err == nil {
			t.Fatal("expected error from broken reader")
		}
		if !errors.Is(err, ioErr) {
			t.Errorf("error should wrap io error, got: %v", err)
		}
	})
}

func TestHasControllers(t *testing.T) {
	t.Run("with controllers", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !hasControllers(dir) {
			t.Error("hasControllers should return true when controllers are present")
		}
	})

	t.Run("empty controllers", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte("\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if hasControllers(dir) {
			t.Error("hasControllers should return false when controllers file is empty")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if hasControllers(t.TempDir()) {
			t.Error("hasControllers should return false when file doesn't exist")
		}
	})
}
