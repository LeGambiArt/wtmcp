// Package diagnostic provides shared diagnostic output for wtmcp CLI tools.
package diagnostic

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/secrets/vault"
)

// VaultPasswordSource returns a human-readable label for how the vault
// password is configured (file, vault IDs, env var, or not configured).
func VaultPasswordSource(cfg *config.Config, resolver func(string) ([]byte, error)) string {
	switch {
	case cfg.Secrets.VaultPasswordFile != "":
		return fmt.Sprintf("file (%s)", cfg.Secrets.VaultPasswordFile)
	case len(cfg.Secrets.VaultIDs) > 0:
		return "vault IDs" //nolint:gosec // status label, not a credential
	default:
		if resolver != nil {
			if pw, err := resolver(""); err == nil {
				vault.ZeroBytes(pw)
				return "env var" //nolint:gosec // status label, not a credential
			}
		}
	}
	return "not configured"
}

// PrintVaultStatus reports vault password configuration and per-group
// encryption status to w.
func PrintVaultStatus(w io.Writer, result *plugin.DiscoveryResult) {
	cfg := result.Config

	_, _ = fmt.Fprintf(w, "vault password: %s\n", VaultPasswordSource(cfg, result.VaultResolver))

	if len(cfg.Secrets.VaultIDs) > 0 {
		_, _ = fmt.Fprintf(w, "vault IDs: %d configured\n", len(cfg.Secrets.VaultIDs))
	}

	if result.EnvDir == "" || result.EnvDirError != "" {
		return
	}

	entries, err := os.ReadDir(result.EnvDir)
	if err != nil {
		return
	}

	resolve := result.VaultResolver
	if resolve == nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".env") {
			continue
		}
		group := strings.TrimSuffix(entry.Name(), ".env")
		path := filepath.Join(result.EnvDir, entry.Name())

		data, err := os.ReadFile(path) //nolint:gosec // diagnostic path from config
		if err != nil {
			continue
		}

		if !vault.IsAnsibleVault(data) {
			continue
		}

		header, err := vault.ParseHeader(strings.SplitN(string(data), "\n", 2)[0])
		if err != nil {
			_, _ = fmt.Fprintf(w, "  - %s (encrypted, invalid header)\n", group)
			continue
		}

		vaultInfo := "vault " + header.Version
		if header.VaultID != "" {
			vaultInfo += " id=" + header.VaultID
		}

		password, err := resolve(header.VaultID)
		if err != nil {
			_, _ = fmt.Fprintf(w, "  - %s (encrypted, %s, no password)\n", group, vaultInfo)
			continue
		}

		plaintext, err := vault.Decrypt(data, password)
		vault.ZeroBytes(password)
		vault.ZeroBytes(plaintext)
		if err != nil {
			_, _ = fmt.Fprintf(w, "  - %s (encrypted, %s, decryption failed)\n", group, vaultInfo)
		} else {
			_, _ = fmt.Fprintf(w, "  - %s (encrypted, %s, decryption ok)\n", group, vaultInfo)
		}
	}

	PrintCredentialFileStatus(w, cfg, resolve)
}

// PrintCredentialFileStatus reports vault-encrypted credential files
// in credentials/<group>/ directories.
func PrintCredentialFileStatus(w io.Writer, cfg *config.Config, resolve func(string) ([]byte, error)) {
	if cfg.CredentialsDir == "" {
		return
	}
	groups, err := os.ReadDir(cfg.CredentialsDir)
	if err != nil {
		return
	}

	var found bool
	for _, group := range groups {
		if !group.IsDir() {
			continue
		}
		groupDir := filepath.Join(cfg.CredentialsDir, group.Name())
		files, err := os.ReadDir(groupDir)
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.IsDir() {
				continue
			}
			path := filepath.Join(groupDir, file.Name())
			f, err := os.Open(path) //nolint:gosec // credentials dir from config
			if err != nil {
				continue
			}
			header := make([]byte, 15)
			n, readErr := f.Read(header)
			_ = f.Close()
			if readErr != nil && n == 0 {
				_, _ = fmt.Fprintf(w, "  - %s/%s (read error: %v)\n", group.Name(), file.Name(), readErr)
				continue
			}
			if !vault.IsAnsibleVault(header[:n]) {
				continue
			}

			if !found {
				_, _ = fmt.Fprintf(w, "credential files:\n")
				found = true
			}

			data, err := os.ReadFile(path) //nolint:gosec // credentials dir from config
			if err != nil {
				_, _ = fmt.Fprintf(w, "  - %s/%s (encrypted, read error)\n", group.Name(), file.Name())
				continue
			}

			hdr, err := vault.ParseHeader(strings.SplitN(string(data), "\n", 2)[0])
			if err != nil {
				_, _ = fmt.Fprintf(w, "  - %s/%s (encrypted, invalid header)\n", group.Name(), file.Name())
				continue
			}

			vaultInfo := "vault " + hdr.Version
			if hdr.VaultID != "" {
				vaultInfo += " id=" + hdr.VaultID
			}

			password, err := resolve(hdr.VaultID)
			if err != nil {
				_, _ = fmt.Fprintf(w, "  - %s/%s (encrypted, %s, no password)\n", group.Name(), file.Name(), vaultInfo)
				continue
			}

			plaintext, err := vault.Decrypt(data, password)
			vault.ZeroBytes(password)
			vault.ZeroBytes(plaintext)
			if err != nil {
				_, _ = fmt.Fprintf(w, "  - %s/%s (encrypted, %s, decryption failed)\n", group.Name(), file.Name(), vaultInfo)
			} else {
				_, _ = fmt.Fprintf(w, "  - %s/%s (encrypted, %s, decryption ok)\n", group.Name(), file.Name(), vaultInfo)
			}
		}
	}
}

// PrintEnvGroups prints sorted env group names and any errors.
func PrintEnvGroups(w io.Writer, result *plugin.DiscoveryResult) {
	_, _ = fmt.Fprintf(w, "env groups: %d\n", len(result.EnvGroups))
	groups := make([]string, 0, len(result.EnvGroups))
	for g := range result.EnvGroups {
		groups = append(groups, g)
	}
	slices.Sort(groups)
	for _, g := range groups {
		_, _ = fmt.Fprintf(w, "  - %s\n", g)
	}
	if result.EnvDirError != "" {
		_, _ = fmt.Fprintf(w, "env.d directory error: %s\n", result.EnvDirError)
	}
	if len(result.EnvErrors) > 0 {
		_, _ = fmt.Fprintf(w, "env group errors: %d\n", len(result.EnvErrors))
		errGroups := make([]string, 0, len(result.EnvErrors))
		for g := range result.EnvErrors {
			errGroups = append(errGroups, g)
		}
		slices.Sort(errGroups)
		for _, g := range errGroups {
			_, _ = fmt.Fprintf(w, "  - %s: %s\n", g, result.EnvErrors[g])
		}
	}
}
