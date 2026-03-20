// wtmcpctl is a command-line utility for managing wtmcp plugins.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set via ldflags at build time.
var (
	Version   = "dev"
	BuildDate = "unknown"
)

var rootCmd = &cobra.Command{
	Use:          "wtmcpctl",
	Short:        "wtmcp plugin management tool",
	Version:      Version,
	SilenceUsage: true,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("wtmcpctl %s (built %s)\n", Version, BuildDate)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&globalWorkdir, "workdir", "",
		"Working directory (default: ~/.config/wtmcp)")
	if err := rootCmd.MarkPersistentFlagDirname("workdir"); err != nil {
		panic(err)
	}

	rootCmd.SetVersionTemplate(
		fmt.Sprintf("wtmcpctl %s (built %s)\n", Version, BuildDate))
	rootCmd.DisableAutoGenTag = true

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		w, _ := cmd.Flags().GetString("workdir")
		setWorkdir(w)
		return nil
	}

	rootCmd.AddCommand(versionCmd, agentCmd, oauthCmd, pluginsCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
