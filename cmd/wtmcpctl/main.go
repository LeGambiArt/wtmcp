// wtmcpctl is a command-line utility for managing wtmcp plugins.
package main

import (
	"fmt"
	"io"
	"log"
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
	rootCmd.PersistentFlags().BoolVarP(&globalVerbose, "verbose", "v", false,
		"Show verbose output (discovery logs, etc.)")
	if err := rootCmd.MarkPersistentFlagDirname("workdir"); err != nil {
		panic(err)
	}

	rootCmd.SetVersionTemplate(
		fmt.Sprintf("wtmcpctl %s (built %s)\n", Version, BuildDate))
	rootCmd.DisableAutoGenTag = true

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		w, _ := cmd.Flags().GetString("workdir")
		setWorkdir(w)

		if !globalVerbose {
			log.SetOutput(io.Discard)
		}
		return nil
	}

	rootCmd.AddCommand(versionCmd, agentCmd, checkCmd, oauthCmd, pluginsCmd, providerCmd, statsCmd, vaultCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
