package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/LeGambiArt/wtmcp/internal/credentials"
)

// credService holds the initialized credential service for CLI commands.
var credService *credentials.Service

var credMgmtCmd = &cobra.Command{
	Use:   "credentials",
	Short: "Manage wtmcp credentials",
	Long: `Manage credentials stored in the OS keyring or env.d files.

Examples:
  wtmcpctl credentials list
  wtmcpctl credentials list jira
  wtmcpctl credentials get jira JIRA_TOKEN
  wtmcpctl credentials set jira JIRA_URL https://jira.example.com
  wtmcpctl credentials delete jira JIRA_TOKEN`,
	PersistentPreRunE: initCredService,
}

func init() {
	credMgmtCmd.AddCommand(
		credListCmd,
		credGetCmd,
		credSetCmd,
		credDeleteCmd,
		credMigrateCmd,
		credExportCmd,
		credImportCmd,
		credTestCmd,
		credRotateCmd,
		credBackupCmd,
		credRestoreCmd,
		credShowConfigCmd,
	)
}

// initCredService initialises the credentials.Service for all
// credential subcommands. It chains into the root PersistentPreRunE
// to ensure globalWorkdir is set.
func initCredService(cmd *cobra.Command, args []string) error {
	// Run the root persistent pre-run to set globalWorkdir.
	if rootCmd.PersistentPreRunE != nil {
		if err := rootCmd.PersistentPreRunE(cmd, args); err != nil {
			return err
		}
	}

	result, err := getDiscoveryResult()
	if err != nil {
		return fmt.Errorf("discover workdir: %w", err)
	}

	envDDir := filepath.Join(result.Workdir, "env.d")
	migrationFile := filepath.Join(result.Workdir, "credentials", ".migration.yaml")

	svc, err := credentials.NewService(envDDir, migrationFile)
	if err != nil {
		return fmt.Errorf("initialise credential service: %w", err)
	}
	credService = svc
	return nil
}
