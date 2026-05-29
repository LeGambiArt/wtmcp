package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/LeGambiArt/wtmcp/internal/diagnostic"
	"github.com/LeGambiArt/wtmcp/internal/plugin"
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

	diagnostic.PrintVaultStatus(os.Stdout, result)
	diagnostic.PrintEnvGroups(os.Stdout, result)

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
