package main

import "path/filepath"

// Codex can inherit a server from a user or parent-project config. When
// disabling it locally, we need the nearest inherited transport for a valid
// disabled override. List parents from outermost to nearest for merge order.
func codexInheritedConfigPaths(configPath, codexHome string) []string {
	paths := []string{filepath.Join(codexHome, "config.toml")}
	project := filepath.Dir(filepath.Dir(configPath))
	var parents []string
	for dir := filepath.Dir(project); dir != project; dir = filepath.Dir(dir) {
		parents = append(parents, filepath.Join(dir, ".codex", "config.toml"))
		if dir == filepath.Dir(dir) {
			break
		}
	}
	for i := len(parents) - 1; i >= 0; i-- {
		paths = append(paths, parents[i])
	}
	return paths
}
