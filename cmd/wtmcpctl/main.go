// wtmcpctl is a command-line utility for managing wtmcp plugins.
package main

import (
	"flag"
	"fmt"
	"os"
)

// Version is set via ldflags at build time.
var (
	Version   = "dev"
	BuildDate = "unknown"
)

func main() {
	// Define top-level flags
	fs := flag.NewFlagSet("wtmcpctl", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "Show version information")
	fs.BoolVar(showVersion, "v", false, "Show version information (short)")
	showHelp := fs.Bool("help", false, "Show help information")
	fs.BoolVar(showHelp, "h", false, "Show help information (short)")
	workdir := fs.String("workdir", "", "Working directory (default: ~/.config/wtmcp)")

	// Custom usage function
	fs.Usage = func() {
		printUsage()
	}

	// Parse top-level flags
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}

	// Handle version flag
	if *showVersion {
		fmt.Printf("wtmcpctl %s (built %s)\n", Version, BuildDate)
		os.Exit(0)
	}

	// Handle help flag
	if *showHelp {
		printUsage()
		os.Exit(0)
	}

	// Set global workdir for plugin discovery
	setWorkdir(*workdir)

	// Get command arguments
	args := fs.Args()
	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	command := args[0]

	// Handle commands
	switch command {
	case "help":
		// Check if help is for a specific command
		if len(args) > 1 {
			handleHelpCommand(args[1])
		} else {
			printUsage()
		}
		os.Exit(0)
	case "version":
		fmt.Printf("wtmcpctl %s (built %s)\n", Version, BuildDate)
		os.Exit(0)
	case "oauth":
		handleOAuthCommand(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

// handleHelpCommand displays help for a specific command.
func handleHelpCommand(cmd string) {
	switch cmd {
	case "oauth":
		printOAuthUsage()
	case "version":
		fmt.Println("Show version information")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  wtmcpctl version")
		fmt.Println("  wtmcpctl -v")
		fmt.Println("  wtmcpctl --version")
	case "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		fmt.Println()
		printUsage()
	}
}

func printUsage() {
	fmt.Printf("wtmcpctl %s - wtmcp plugin management tool\n", Version)
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  wtmcpctl [global-options] <command> [options]")
	fmt.Println()
	fmt.Println("Global Options:")
	fmt.Println("  --workdir <path>    Working directory (default: ~/.config/wtmcp)")
	fmt.Println("  -v, --version       Show version information")
	fmt.Println("  -h, --help          Show this help message")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  oauth        Manage OAuth authentication for plugins")
	fmt.Println("  version      Show version information")
	fmt.Println("  help         Show this help message")
	fmt.Println()
	fmt.Println("Use 'wtmcpctl <command> --help' for more information about a command.")
}

