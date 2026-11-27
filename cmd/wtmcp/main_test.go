package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersion(t *testing.T) {
	if Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestIsHomeDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	if !isHomeDir(home) {
		t.Errorf("isHomeDir(%q) should be true", home)
	}

	project := filepath.Join(home, "projects", "test")
	if isHomeDir(project) {
		t.Errorf("isHomeDir(%q) should be false", project)
	}

	if isHomeDir("/") {
		t.Error("isHomeDir(/) should be false")
	}
}

func TestDirWithin(t *testing.T) {
	tests := []struct {
		parent, dir string
		want        bool
	}{
		{"/home/u/proj", "/home/u/proj", true},
		{"/home/u/proj", "/home/u/proj/credentials", true},
		{"/home/u/proj", "/home/u/proj/sub/env.d", true},
		{"/home/u", "/home/u2/credentials", false},
		{"/home/u/proj", "/home/u/project2", false},
		{"/home/u/proj", "/etc", false},
		{"", "/home/u/proj", false},
		{"/home/u/proj", "", false},
	}
	for _, tt := range tests {
		if got := dirWithin(tt.parent, tt.dir); got != tt.want {
			t.Errorf("dirWithin(%q, %q) = %v, want %v", tt.parent, tt.dir, got, tt.want)
		}
	}
}
