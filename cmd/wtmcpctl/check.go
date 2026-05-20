package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
	"github.com/LeGambiArt/wtmcp/internal/secrets/vault"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Print diagnostic info about config and plugins",
	Args:  cobra.NoArgs,
	RunE:  runCtlCheck,
}

func runCtlCheck(_ *cobra.Command, _ []string) error {
	result, err := plugin.Discover(plugin.DiscoveryOptions{
		WorkdirOverride: globalWorkdir,
	})
	if err != nil {
		return err
	}
	defer result.Close()

	fmt.Printf("wtmcpctl %s\n", Version)
	fmt.Printf("workdir: %s\n", result.Workdir)
	if len(result.Config.Plugins.Enabled) > 0 {
		fmt.Printf("plugin mode: allowlist (%d plugins)\n", len(result.Config.Plugins.Enabled))
	} else {
		fmt.Printf("plugin mode: default\n")
	}
	fmt.Printf("user plugins: %v\n", result.Config.Plugins.UserPlugins)

	printCtlVaultStatus(result)

	fmt.Printf("env groups: %d\n", len(result.EnvGroups))
	for group := range result.EnvGroups {
		fmt.Printf("  - %s\n", group)
	}
	if result.EnvDirError != "" {
		fmt.Printf("env.d directory error: %s\n", result.EnvDirError)
	}
	if len(result.EnvErrors) > 0 {
		fmt.Printf("env group errors: %d\n", len(result.EnvErrors))
		for group, msg := range result.EnvErrors {
			fmt.Printf("  - %s: %s\n", group, msg)
		}
	}

	fmt.Printf("\nplugin search path:\n")
	for i, dir := range result.Config.PluginDirs {
		exists := "missing"
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			exists = "ok"
		}
		fmt.Printf("  %d. %s [%s]\n", i+1, dir, exists)
	}

	manifests := result.Manager.Manifests()
	fmt.Printf("\ndiscovered plugins: %d\n", len(manifests))
	var totalPrimary, totalDeferred int
	for _, m := range manifests {
		var primaryCount, deferredCount int
		for _, t := range m.Tools {
			if t.IsPrimary() {
				primaryCount++
			} else {
				deferredCount++
			}
		}
		totalPrimary += primaryCount
		totalDeferred += deferredCount
		fmt.Printf("  - %s v%s (%s)\n", m.Name, m.Version, m.Dir)
		fmt.Printf("    handler: %s | execution: %s | tools: %d (primary: %d, deferred: %d)\n",
			m.Handler, m.Execution, len(m.Tools), primaryCount, deferredCount)
	}

	fmt.Printf("\ntool discovery: %s\n", result.Config.Tools.Discovery)
	fmt.Printf("primary tools: %d\n", totalPrimary)
	fmt.Printf("deferred tools: %d\n", totalDeferred)

	if len(manifests) == 0 {
		fmt.Println("\nno plugins found. check that plugin directories contain")
		fmt.Println("subdirectories with plugin.yaml files.")
	}

	return nil
}

// printCtlVaultStatus reports vault password configuration and per-group
// encryption status for the check command.
func printCtlVaultStatus(result *plugin.DiscoveryResult) {
	cfg := result.Config

	passwordSource := "not configured"
	switch {
	case cfg.Secrets.VaultPasswordFile != "":
		passwordSource = fmt.Sprintf("file (%s)", cfg.Secrets.VaultPasswordFile)
	case len(cfg.Secrets.VaultIDs) > 0:
		passwordSource = "vault IDs" //nolint:gosec // status label, not a credential
	default:
		if result.VaultResolver != nil {
			if pw, err := result.VaultResolver(""); err == nil {
				vault.ZeroBytes(pw)
				passwordSource = "env var" //nolint:gosec // status label, not a credential
			}
		}
	}
	fmt.Printf("vault password: %s\n", passwordSource)

	if len(cfg.Secrets.VaultIDs) > 0 {
		fmt.Printf("vault IDs: %d configured\n", len(cfg.Secrets.VaultIDs))
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

		data, err := os.ReadFile(path) //nolint:gosec // check path from config
		if err != nil {
			continue
		}

		if !vault.IsAnsibleVault(data) {
			continue
		}

		header, err := vault.ParseHeader(strings.SplitN(string(data), "\n", 2)[0])
		if err != nil {
			fmt.Printf("  - %s (encrypted, invalid header)\n", group)
			continue
		}

		vaultInfo := "vault " + header.Version
		if header.VaultID != "" {
			vaultInfo += " id=" + header.VaultID
		}

		password, err := resolve(header.VaultID)
		if err != nil {
			fmt.Printf("  - %s (encrypted, %s, no password)\n", group, vaultInfo)
			continue
		}

		plaintext, err := vault.Decrypt(data, password)
		vault.ZeroBytes(password)
		vault.ZeroBytes(plaintext)
		if err != nil {
			fmt.Printf("  - %s (encrypted, %s, decryption failed)\n", group, vaultInfo)
		} else {
			fmt.Printf("  - %s (encrypted, %s, decryption ok)\n", group, vaultInfo)
		}
	}

	printCtlCredentialFileStatus(cfg, resolve)
}

// printCtlCredentialFileStatus reports vault-encrypted credential files
// in credentials/<group>/ directories.
func printCtlCredentialFileStatus(cfg *config.Config, resolve func(string) ([]byte, error)) {
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
			n, _ := f.Read(header)
			_ = f.Close()
			if !vault.IsAnsibleVault(header[:n]) {
				continue
			}

			if !found {
				fmt.Printf("credential files:\n")
				found = true
			}

			data, err := os.ReadFile(path) //nolint:gosec // credentials dir from config
			if err != nil {
				fmt.Printf("  - %s/%s (encrypted, read error)\n", group.Name(), file.Name())
				continue
			}

			hdr, err := vault.ParseHeader(strings.SplitN(string(data), "\n", 2)[0])
			if err != nil {
				fmt.Printf("  - %s/%s (encrypted, invalid header)\n", group.Name(), file.Name())
				continue
			}

			vaultInfo := "vault " + hdr.Version
			if hdr.VaultID != "" {
				vaultInfo += " id=" + hdr.VaultID
			}

			password, err := resolve(hdr.VaultID)
			if err != nil {
				fmt.Printf("  - %s/%s (encrypted, %s, no password)\n", group.Name(), file.Name(), vaultInfo)
				continue
			}

			plaintext, err := vault.Decrypt(data, password)
			vault.ZeroBytes(password)
			vault.ZeroBytes(plaintext)
			if err != nil {
				fmt.Printf("  - %s/%s (encrypted, %s, decryption failed)\n", group.Name(), file.Name(), vaultInfo)
			} else {
				fmt.Printf("  - %s/%s (encrypted, %s, decryption ok)\n", group.Name(), file.Name(), vaultInfo)
			}
		}
	}
}
