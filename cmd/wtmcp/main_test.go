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
