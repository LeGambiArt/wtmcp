package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	xterm "golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/LeGambiArt/wtmcp/internal/credentials"
)

// ── list ────────────────────────────────────────────────────────

var credListCmd = &cobra.Command{
	Use:   "list [group]",
	Short: "List credential groups or keys within a group",
	Long: `Without arguments, show all credential groups and their status.
With a group name, show all keys in that group and their sources.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCredList,
}

func init() {
	credListCmd.Flags().BoolP("plain", "p", false,
		"Plain text output (no colours or borders)")
}

func runCredList(cmd *cobra.Command, args []string) error {
	plain, _ := cmd.Flags().GetBool("plain")

	if len(args) == 1 {
		return runCredListGroup(args[0], plain)
	}
	return runCredListAll(plain)
}

// runCredListAll shows all credential groups with their migration
// status and key counts.
func runCredListAll(plain bool) error {
	groups := credService.ListGroups()
	if len(groups) == 0 {
		fmt.Println("No credential groups found.")
		return nil
	}
	sort.Strings(groups)

	if plain {
		for _, g := range groups {
			info := credService.GetGroupInfo(g)
			status := "not migrated"
			if info.Migrated {
				status = "migrated"
			}
			totalKeys := countUniqueKeys(info)
			fmt.Printf("%s\t%s\t%d keys\n", g, status, totalKeys)
		}
		return nil
	}

	migratedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	notMigratedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	rows := make([][]string, 0, len(groups))
	for _, g := range groups {
		info := credService.GetGroupInfo(g)

		var statusCell string
		if info.Migrated {
			statusCell = migratedStyle.Render("migrated")
		} else {
			statusCell = notMigratedStyle.Render("not migrated")
		}

		// Build a summary of key counts per source.
		summary := keyCountSummary(info)

		rows = append(rows, []string{g, statusCell, summary})
	}

	w, _, _ := term.GetSize(os.Stdout.Fd())
	if w <= 0 {
		w = 80
	}

	t := table.New().
		Width(w).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return lipgloss.NewStyle()
		}).
		Headers("Group", "Status", "Keys").
		Rows(rows...)

	fmt.Println(t)
	fmt.Printf("\nenv.d directory: %s\n", credService.EnvDDir())
	return nil
}

// runCredListGroup shows all keys in a specific group with their sources.
func runCredListGroup(group string, plain bool) error {
	info := credService.GetGroupInfo(group)

	// Collect all keys with their sources.
	type keyEntry struct {
		Key    string
		Source string
	}
	seen := make(map[string]bool)
	var entries []keyEntry

	for _, k := range info.EnvDKeys {
		if !seen[k] {
			seen[k] = true
			entries = append(entries, keyEntry{Key: k, Source: "env.d"})
		}
	}
	for _, k := range info.KeyringKeys {
		if seen[k] {
			// Key exists in both env.d and keyring -- show the one that
			// takes precedence.
			for i, e := range entries {
				if e.Key == k {
					if info.Migrated {
						entries[i].Source = "keyring (primary), env.d"
					} else {
						entries[i].Source = "env.d (primary), keyring"
					}
					break
				}
			}
		} else {
			seen[k] = true
			entries = append(entries, keyEntry{Key: k, Source: "keyring"})
		}
	}

	if len(entries) == 0 {
		fmt.Printf("No credentials found for group %q.\n", group)
		return nil
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})

	if plain {
		status := "not migrated"
		if info.Migrated {
			status = "migrated"
		}
		fmt.Printf("# group: %s [%s]\n", group, status)
		for _, e := range entries {
			fmt.Printf("%s\t%s\n", e.Key, e.Source)
		}
		return nil
	}

	status := "not migrated"
	if info.Migrated {
		status = "migrated"
	}
	fmt.Printf("Credential Group: %s [%s]\n\n", group, status)

	rows := make([][]string, len(entries))
	for i, e := range entries {
		rows[i] = []string{e.Key, e.Source}
	}

	w, _, _ := term.GetSize(os.Stdout.Fd())
	if w <= 0 {
		w = 80
	}

	t := table.New().
		Width(w).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle
			}
			return lipgloss.NewStyle()
		}).
		Headers("Key", "Source").
		Rows(rows...)

	fmt.Println(t)
	return nil
}

// ── get ─────────────────────────────────────────────────────────

var credGetCmd = &cobra.Command{
	Use:   "get <group> <key>",
	Short: "Get a credential value",
	Long: `Retrieve a credential by group and key.

By default, the value is masked. Use --show-value to display the
full value, or --source-only to show only the source.`,
	Args: cobra.ExactArgs(2),
	RunE: runCredGet,
}

func init() {
	credGetCmd.Flags().Bool("show-value", false,
		"Show the full credential value (default: masked)")
	credGetCmd.Flags().Bool("source-only", false,
		"Only show the credential source, not the value")
}

func runCredGet(cmd *cobra.Command, args []string) error {
	group, key := args[0], args[1]
	showValue, _ := cmd.Flags().GetBool("show-value")
	sourceOnly, _ := cmd.Flags().GetBool("source-only")

	value, source, err := credService.Get(group, key)
	if err != nil {
		if errors.Is(err, credentials.ErrCredentialNotFound) {
			return fmt.Errorf("credential %s/%s not found", group, key)
		}
		return fmt.Errorf("get credential: %w", err)
	}

	if sourceOnly {
		fmt.Println(source)
		return nil
	}

	displayed := maskValue(value)
	if showValue {
		displayed = value
	}

	fmt.Println(key)
	fmt.Printf("  Value:  %s\n", displayed)
	fmt.Printf("  Source: %s\n", source)
	return nil
}

// ── set ─────────────────────────────────────────────────────────

var credSetCmd = &cobra.Command{
	Use:   "set <group> <key>",
	Short: "Set a credential value",
	Long: `Store a credential in the keyring.

You will be prompted to enter the value interactively (input is hidden).
To set a value non-interactively, pipe the value to stdin.

The group must be migrated to the keyring, or use --keyring to force
keyring storage.`,
	Args: cobra.ExactArgs(2),
	RunE: runCredSet,
}

func init() {
	credSetCmd.Flags().Bool("keyring", false,
		"Force storage in keyring (even if group is not migrated)")
}

func runCredSet(cmd *cobra.Command, args []string) error {
	group, key := args[0], args[1]
	forceKeyring, _ := cmd.Flags().GetBool("keyring")

	value, err := promptHiddenInput(fmt.Sprintf("Enter value for %s: ", key))
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}

	if err := credService.Set(group, key, value, forceKeyring); err != nil {
		return fmt.Errorf("set credential: %w", err)
	}

	fmt.Printf("Credential %s/%s stored in keyring.\n", group, key)
	return nil
}

// ── delete ──────────────────────────────────────────────────────

var credDeleteCmd = &cobra.Command{
	Use:   "delete <group> <key>",
	Short: "Delete a credential",
	Long: `Remove a credential from the keyring and/or env.d file.

Prompts for confirmation unless --force is used. Use --keyring-only
or --env-d-only to target a specific store.`,
	Args: cobra.ExactArgs(2),
	RunE: runCredDelete,
}

func init() {
	credDeleteCmd.Flags().Bool("keyring-only", false,
		"Only delete from keyring")
	credDeleteCmd.Flags().Bool("env-d-only", false,
		"Only delete from env.d file")
	credDeleteCmd.Flags().BoolP("force", "f", false,
		"Skip confirmation prompt")
}

func runCredDelete(cmd *cobra.Command, args []string) error {
	group, key := args[0], args[1]
	keyringOnly, _ := cmd.Flags().GetBool("keyring-only")
	envDOnly, _ := cmd.Flags().GetBool("env-d-only")
	force, _ := cmd.Flags().GetBool("force")

	if keyringOnly && envDOnly {
		return fmt.Errorf("cannot use both --keyring-only and --env-d-only")
	}

	// Confirm deletion.
	if !force {
		if !promptYesNo(fmt.Sprintf("Delete credential %s/%s? [y/N] ", group, key)) {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	switch {
	case keyringOnly:
		if err := credService.DeleteFromKeyring(group, key); err != nil {
			if errors.Is(err, credentials.ErrCredentialNotFound) {
				return fmt.Errorf("credential %s/%s not found in keyring", group, key)
			}
			return fmt.Errorf("delete from keyring: %w", err)
		}
		fmt.Println("Deleted from keyring.")

	case envDOnly:
		if err := credService.DeleteFromEnvD(group, key); err != nil {
			if errors.Is(err, credentials.ErrCredentialNotFound) {
				return fmt.Errorf("credential %s/%s not found in env.d", group, key)
			}
			return fmt.Errorf("delete from env.d: %w", err)
		}
		fmt.Println("Deleted from env.d.")

	default:
		sources, err := credService.Delete(group, key)
		if err != nil {
			if errors.Is(err, credentials.ErrCredentialNotFound) {
				return fmt.Errorf("credential %s/%s not found", group, key)
			}
			return fmt.Errorf("delete credential: %w", err)
		}
		for _, src := range sources {
			fmt.Printf("Deleted from %s.\n", src)
		}
	}

	return nil
}

// ── migrate ────────────────────────────────────────────────────

var credMigrateCmd = &cobra.Command{
	Use:   "migrate <group>",
	Short: "Migrate credentials from env.d to keyring",
	Long: `Migrate all credentials for a group from env.d files to the
OS keyring. The migration is atomic: all keys are stored in the
keyring before the group is marked as migrated.

Optionally creates an encrypted backup before migration and
deletes the env.d file after successful migration.`,
	Args: cobra.ExactArgs(1),
	RunE: runCredMigrate,
}

func init() {
	credMigrateCmd.Flags().Bool("delete-env-d", false,
		"Automatically delete env.d file after migration (no prompt)")
	credMigrateCmd.Flags().Bool("skip-validation", false,
		"Don't run validation tool after migration")
	credMigrateCmd.Flags().Bool("dry-run", false,
		"Show what would be migrated without doing it")
}

func runCredMigrate(cmd *cobra.Command, args []string) error {
	group := args[0]
	deleteEnvD, _ := cmd.Flags().GetBool("delete-env-d")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Check if the group is already migrated.
	if credService.GetMigrationState().IsMigrated(group) {
		return fmt.Errorf("group %q is already migrated", group)
	}

	// Check if keyring is available.
	if !credService.IsKeyringAvailable() {
		return fmt.Errorf("keyring is not available; cannot migrate")
	}

	// Check if env.d file exists and read credentials.
	envDPath := filepath.Join(credService.EnvDDir(), group+".env")
	creds := credService.GetAll(group)
	if len(creds) == 0 {
		return fmt.Errorf("no credentials found for group %q in %s", group, envDPath)
	}

	// Sort keys for consistent display.
	keys := make([]string, 0, len(creds))
	for k := range creds {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("Reading credentials from env.d/%s.env\n", group)
	fmt.Printf("Found %d keys: %s\n", len(keys), strings.Join(keys, ", "))

	if dryRun {
		fmt.Println("\n[dry-run] The following would be migrated to keyring:")
		for _, k := range keys {
			fmt.Printf("  %s\n", k)
		}
		fmt.Println("\n[dry-run] No changes were made.")
		return nil
	}

	// Prompt for backup.
	fmt.Println()
	if promptYesNoDefault("Create backup before migration? [Y/n] ", true) {
		password, err := promptPasswordBytes("Enter backup password: ", "Confirm password: ")
		if err != nil {
			return err
		}

		if err := credService.CreateBackup(password, ""); err != nil {
			zeroSlice(password)
			return fmt.Errorf("create backup: %w", err)
		}
		zeroSlice(password)
		fmt.Println("Backup created.")
	}

	// Store all keys in keyring atomically.
	fmt.Println("\nStoring credentials in keyring...")
	var stored []string
	for _, key := range keys {
		if err := credService.Set(group, key, creds[key], true); err != nil {
			// Rollback: delete all keys written so far.
			for _, sk := range stored {
				_ = credService.DeleteFromKeyring(group, sk)
			}
			return fmt.Errorf("store %s in keyring (rolled back): %w", key, err)
		}
		stored = append(stored, key)
		fmt.Printf("  + %s\n", key)
	}

	// Mark group as migrated.
	if err := credService.GetMigrationState().MarkMigrated(group); err != nil {
		return fmt.Errorf("mark migrated: %w", err)
	}
	fmt.Printf("\nMarked %q as migrated in .migration.yaml\n", group)

	// Handle env.d file deletion.
	if _, err := os.Stat(envDPath); err == nil {
		if deleteEnvD {
			if err := os.Remove(envDPath); err != nil {
				return fmt.Errorf("delete %s: %w", envDPath, err)
			}
			fmt.Printf("Deleted %s\n", envDPath)
		} else {
			fmt.Println()
			if promptYesNo("Delete env.d/" + group + ".env? [y/N] ") {
				if err := os.Remove(envDPath); err != nil {
					return fmt.Errorf("delete %s: %w", envDPath, err)
				}
				fmt.Printf("Deleted %s\n", envDPath)
			} else {
				fmt.Printf("Migration complete. You can delete %s manually when ready.\n", envDPath)
			}
		}
	}

	return nil
}

// ── export ─────────────────────────────────────────────────────

var credExportCmd = &cobra.Command{
	Use:   "export <group> [output_file]",
	Short: "Export credentials to a file or stdout",
	Long: `Export all credentials for a group to a file or stdout.

Reads from keyring (if migrated) or env.d (if not). Output
format can be env, json, or yaml. Files are created with 0600
permissions.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runCredExport,
}

func init() {
	credExportCmd.Flags().Bool("stdout", false,
		"Print to stdout instead of file")
	credExportCmd.Flags().String("format", "env",
		"Output format: env, json, yaml")
}

func runCredExport(cmd *cobra.Command, args []string) error {
	group := args[0]
	toStdout, _ := cmd.Flags().GetBool("stdout")
	format, _ := cmd.Flags().GetString("format")

	// Validate format.
	switch format {
	case "env", "json", "yaml":
		// ok
	default:
		return fmt.Errorf("unsupported format %q (use env, json, or yaml)", format)
	}

	// Get all credentials for the group.
	creds := credService.GetAll(group)
	if len(creds) == 0 {
		return fmt.Errorf("no credentials found for group %q", group)
	}

	// Format output.
	output, err := formatCredentials(creds, format)
	if err != nil {
		return fmt.Errorf("format credentials: %w", err)
	}

	if toStdout {
		fmt.Print(output)
		return nil
	}

	// Determine output path.
	var outputPath string
	if len(args) == 2 {
		outputPath = args[1]
	} else {
		outputPath = filepath.Join(credService.EnvDDir(), group+".env")
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if err := os.WriteFile(outputPath, []byte(output), 0o600); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	fmt.Printf("Exported %d credentials to %s\n", len(creds), outputPath)
	return nil
}

// ── import ─────────────────────────────────────────────────────

var credImportCmd = &cobra.Command{
	Use:   "import <group> <input_file>",
	Short: "Import credentials from a file",
	Long: `Import credentials from a file into the keyring.

Supports env, json, and yaml formats. Format is auto-detected
from the file extension, or can be specified explicitly.`,
	Args: cobra.ExactArgs(2),
	RunE: runCredImport,
}

func init() {
	credImportCmd.Flags().Bool("mark-migrated", false,
		"Mark group as migrated after import")
	credImportCmd.Flags().String("format", "",
		"Input format: env, json, yaml (auto-detected if omitted)")
	credImportCmd.Flags().Bool("overwrite", false,
		"Overwrite existing keys without prompting")
}

func runCredImport(cmd *cobra.Command, args []string) error {
	group, inputPath := args[0], args[1]
	markMigrated, _ := cmd.Flags().GetBool("mark-migrated")
	format, _ := cmd.Flags().GetString("format")
	overwrite, _ := cmd.Flags().GetBool("overwrite")

	// Read input file.
	data, err := os.ReadFile(inputPath) //nolint:gosec // user-provided path
	if err != nil {
		return fmt.Errorf("read %s: %w", inputPath, err)
	}

	// Detect format if not specified.
	if format == "" {
		format = detectFormatFromExtension(inputPath)
	}

	// Parse credentials.
	creds, err := parseCredentials(data, format)
	if err != nil {
		return fmt.Errorf("parse %s as %s: %w", inputPath, format, err)
	}

	if len(creds) == 0 {
		return fmt.Errorf("no credentials found in %s", inputPath)
	}

	// Sort keys for consistent output.
	keys := make([]string, 0, len(creds))
	for k := range creds {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("Importing %d credentials for group %q from %s\n", len(keys), group, inputPath)

	// Determine if we store in keyring (migrated or markMigrated) or refuse.
	isMigrated := credService.GetMigrationState().IsMigrated(group)
	storeInKeyring := isMigrated || markMigrated

	if !storeInKeyring {
		return fmt.Errorf("group %q is not migrated; use --mark-migrated to store in keyring", group)
	}

	// Check for existing keys and handle overwrite.
	for _, key := range keys {
		_, _, existErr := credService.Get(group, key)
		exists := existErr == nil

		if exists && !overwrite {
			if !promptYesNo(fmt.Sprintf("Overwrite existing key %s/%s? [y/N] ", group, key)) {
				fmt.Printf("  - %s (skipped)\n", key)
				continue
			}
		}

		if err := credService.Set(group, key, creds[key], true); err != nil {
			return fmt.Errorf("store %s: %w", key, err)
		}
		fmt.Printf("  + %s\n", key)
	}

	// Mark as migrated if requested.
	if markMigrated && !isMigrated {
		if err := credService.GetMigrationState().MarkMigrated(group); err != nil {
			return fmt.Errorf("mark migrated: %w", err)
		}
		fmt.Printf("Marked %q as migrated.\n", group)
	}

	fmt.Println("Import complete.")
	return nil
}

// ── format helpers ─────────────────────────────────────────────

// formatCredentials serialises a credential map to the given format.
func formatCredentials(creds map[string]string, format string) (string, error) {
	// Sort keys for deterministic output.
	keys := make([]string, 0, len(creds))
	for k := range creds {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	switch format {
	case "env":
		var sb strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&sb, "%s=%s\n", k, creds[k])
		}
		return sb.String(), nil

	case "json":
		// Build ordered map by using sorted keys into a
		// map that json.MarshalIndent will serialise.
		data, err := json.MarshalIndent(creds, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal json: %w", err)
		}
		return string(data) + "\n", nil

	case "yaml":
		// Use a yaml.Node to preserve key order.
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range keys {
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: k},
				&yaml.Node{Kind: yaml.ScalarNode, Value: creds[k]},
			)
		}
		data, err := yaml.Marshal(node)
		if err != nil {
			return "", fmt.Errorf("marshal yaml: %w", err)
		}
		return string(data), nil

	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}
}

// parseCredentials parses credential data from the given format.
func parseCredentials(data []byte, format string) (map[string]string, error) {
	switch format {
	case "env":
		return parseEnvFormat(data)
	case "json":
		return parseJSONFormat(data)
	case "yaml":
		return parseYAMLFormat(data)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}

// parseEnvFormat parses KEY=VALUE lines, ignoring comments and
// blank lines. Supports optional "export " prefix and quoted values.
func parseEnvFormat(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		// Strip surrounding quotes.
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = value[1 : len(value)-1]
		}
		result[key] = value
	}
	return result, scanner.Err()
}

// parseJSONFormat parses a JSON object with string keys and values.
func parseJSONFormat(data []byte) (map[string]string, error) {
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// parseYAMLFormat parses a YAML mapping with string keys and values.
func parseYAMLFormat(data []byte) (map[string]string, error) {
	var result map[string]string
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// detectFormatFromExtension guesses the credential file format from
// the file extension. Returns "env" as the default.
func detectFormatFromExtension(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "env"
	}
}

// promptYesNoDefault displays a prompt and returns the default value
// if the user presses Enter without typing anything. "y" or "yes"
// returns true; "n" or "no" returns false.
func promptYesNoDefault(prompt string, defaultYes bool) bool {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return defaultYes
	}
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}

// ── test ───────────────────────────────────────────────────────

var credTestCmd = &cobra.Command{
	Use:   "test <group>",
	Short: "Test credential resolution for a group",
	Long: `Verify that credentials can be resolved for a group.

This does not run a validation tool -- it only checks that at least
one credential can be found via env.d, keyring, or environment
variables.`,
	Args: cobra.ExactArgs(1),
	RunE: runCredTest,
}

func runCredTest(_ *cobra.Command, args []string) error {
	group := args[0]

	creds := credService.GetAll(group)
	if len(creds) == 0 {
		return fmt.Errorf("no credentials found for group %q", group)
	}

	// Sort keys for consistent output.
	keys := make([]string, 0, len(creds))
	for k := range creds {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("Found %d credentials for '%s':\n", len(creds), group)
	for _, k := range keys {
		fmt.Printf("  %s = %s\n", k, maskValue(creds[k]))
	}
	return nil
}

// ── rotate ─────────────────────────────────────────────────────

var credRotateCmd = &cobra.Command{
	Use:   "rotate <group> <key>",
	Short: "Rotate a credential value",
	Long: `Prompt for a new value and update the credential in the keyring.

The new value is entered twice (with hidden input) to confirm. The
credential must exist before it can be rotated.`,
	Args: cobra.ExactArgs(2),
	RunE: runCredRotate,
}

func runCredRotate(_ *cobra.Command, args []string) error {
	group, key := args[0], args[1]

	// Verify the credential exists.
	_, _, err := credService.Get(group, key)
	if err != nil {
		if errors.Is(err, credentials.ErrCredentialNotFound) {
			return fmt.Errorf("credential %s/%s not found", group, key)
		}
		return fmt.Errorf("get credential: %w", err)
	}

	// Prompt for new value.
	newValue, err := promptHiddenInput(fmt.Sprintf("Enter new value for %s: ", key))
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	if newValue == "" {
		return fmt.Errorf("value cannot be empty")
	}

	// Confirm.
	confirm, err := promptHiddenInput("Confirm new value: ")
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}

	if !bytes.Equal([]byte(newValue), []byte(confirm)) {
		return fmt.Errorf("values do not match")
	}

	// Update: use keyring if the group is migrated.
	forceKeyring := credService.GetMigrationState().IsMigrated(group)
	if err := credService.Set(group, key, newValue, forceKeyring); err != nil {
		return fmt.Errorf("set credential: %w", err)
	}

	fmt.Printf("Rotated %s/%s\n", group, key)
	return nil
}

// ── backup ─────────────────────────────────────────────────────

var credBackupCmd = &cobra.Command{
	Use:   "backup [output_file]",
	Short: "Create an encrypted backup of keyring credentials",
	Long: `Export all migrated keyring credentials to an encrypted backup file.

The backup is encrypted with AES-256-GCM using a key derived from a
password via Argon2id. If no output file is specified, the backup is
written to ~/.config/wtmcp/credentials/backup-<timestamp>.enc.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCredBackup,
}

func runCredBackup(_ *cobra.Command, args []string) error {
	password, err := promptPasswordBytes("Enter backup password: ", "Confirm password: ")
	if err != nil {
		return err
	}
	defer zeroSlice(password)

	// Determine output path.
	var outputPath string
	if len(args) == 1 {
		outputPath = args[0]
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home directory: %w", err)
		}
		credDir := filepath.Join(homeDir, ".config", "wtmcp", "credentials")
		timestamp := time.Now().UTC().Format("20060102-150405")
		outputPath = filepath.Join(credDir, fmt.Sprintf("backup-%s.enc", timestamp))
	}

	if err := credService.CreateBackup(password, outputPath); err != nil {
		return fmt.Errorf("create backup: %w", err)
	}

	fmt.Printf("Backup created: %s\n", outputPath)
	return nil
}

// ── restore ────────────────────────────────────────────────────

var credRestoreCmd = &cobra.Command{
	Use:   "restore <backup_file>",
	Short: "Restore credentials from an encrypted backup",
	Long: `Import credentials from an encrypted backup file into the keyring.

By default, only missing keys are imported (merge mode). Use --overwrite
to replace existing keys with values from the backup.`,
	Args: cobra.ExactArgs(1),
	RunE: runCredRestore,
}

func init() {
	credRestoreCmd.Flags().Bool("overwrite", false,
		"Overwrite existing keys (default: merge, import only missing keys)")
}

func runCredRestore(cmd *cobra.Command, args []string) error {
	overwrite, _ := cmd.Flags().GetBool("overwrite")

	password, err := promptPasswordBytes("Enter backup password: ", "")
	if err != nil {
		return err
	}
	defer zeroSlice(password)

	mergeMode := !overwrite

	if err := credService.RestoreBackup(args[0], password, mergeMode); err != nil {
		return fmt.Errorf("restore backup: %w", err)
	}

	mode := "merge"
	if overwrite {
		mode = "overwrite"
	}
	fmt.Printf("Credentials restored from backup (mode: %s)\n", mode)
	return nil
}

// ── show-config ────────────────────────────────────────────────

var credShowConfigCmd = &cobra.Command{
	Use:   "show-config",
	Short: "Show credential system configuration and status",
	Long: `Display keyring availability, credential groups, migration status,
and key counts per source.`,
	Args: cobra.NoArgs,
	RunE: runCredShowConfig,
}

func runCredShowConfig(_ *cobra.Command, _ []string) error {
	// Keyring status.
	if credService.IsKeyringAvailable() {
		fmt.Println("Keyring: Available")
	} else {
		fmt.Println("Keyring: Not available")
	}
	fmt.Printf("env.d directory: %s\n", credService.EnvDDir())

	// List groups.
	groups := credService.ListGroups()
	sort.Strings(groups)

	migratedGroups := credService.GetMigrationState().GetMigratedGroups()
	migratedSet := make(map[string]bool, len(migratedGroups))
	for _, g := range migratedGroups {
		migratedSet[g] = true
	}

	if len(groups) == 0 {
		fmt.Println("\nNo credential groups found.")
		return nil
	}

	fmt.Println("\nCredential Groups:")
	for _, group := range groups {
		info := credService.GetGroupInfo(group)

		status := "not migrated"
		if migratedSet[group] {
			status = "migrated"
		}

		totalKeys := countUniqueKeys(info)
		summary := keyCountSummary(info)
		fmt.Printf("  %s [%s] - %d keys (%s)\n", group, status, totalKeys, summary)
	}

	return nil
}

// ── helpers ─────────────────────────────────────────────────────

// maskValue returns a masked version of a credential value,
// showing only the first 4 characters followed by "...".
func maskValue(value string) string {
	if len(value) <= 4 {
		return "***"
	}
	return value[:4] + "..."
}

// countUniqueKeys returns the number of unique keys across
// env.d and keyring for a group.
func countUniqueKeys(info credentials.GroupInfo) int {
	seen := make(map[string]bool)
	for _, k := range info.EnvDKeys {
		seen[k] = true
	}
	for _, k := range info.KeyringKeys {
		seen[k] = true
	}
	return len(seen)
}

// keyCountSummary builds a human-readable summary of key counts
// per source for a credential group.
func keyCountSummary(info credentials.GroupInfo) string {
	var parts []string
	if len(info.KeyringKeys) > 0 {
		parts = append(parts, fmt.Sprintf("%d in keyring", len(info.KeyringKeys)))
	}
	if len(info.EnvDKeys) > 0 {
		parts = append(parts, fmt.Sprintf("%d in env.d", len(info.EnvDKeys)))
	}
	if len(parts) == 0 {
		return "0 keys"
	}
	return strings.Join(parts, ", ")
}

// promptHiddenInput prompts the user for input with hidden echo
// (for passwords and secrets). Uses charmbracelet/huh when a
// terminal is available, falls back to plain bufio.Reader otherwise.
func promptHiddenInput(prompt string) (string, error) {
	// Check if stdin is a terminal.
	if term.IsTerminal(os.Stdin.Fd()) {
		var value string
		err := huh.NewInput().
			Title(prompt).
			EchoMode(huh.EchoModePassword).
			Value(&value).
			Run()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}

	// Non-interactive: read from stdin.
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptPasswordBytes prompts for a password and returns it as []byte so
// callers can zero it after use. When confirmLabel is non-empty the user
// must enter the password twice and both entries must match.
func promptPasswordBytes(label, confirmLabel string) ([]byte, error) {
	fmt.Fprint(os.Stderr, label)
	pass, err := xterm.ReadPassword(syscall.Stdin)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	if len(pass) == 0 {
		return nil, fmt.Errorf("password must not be empty")
	}

	if confirmLabel != "" {
		fmt.Fprint(os.Stderr, confirmLabel)
		confirm, err := xterm.ReadPassword(syscall.Stdin)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			zeroSlice(pass)
			return nil, fmt.Errorf("read confirmation: %w", err)
		}
		if !bytes.Equal(pass, confirm) {
			zeroSlice(pass)
			zeroSlice(confirm)
			return nil, fmt.Errorf("passwords do not match")
		}
		zeroSlice(confirm)
	}

	return pass, nil
}

func zeroSlice(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
