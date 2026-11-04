package main

import (
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/BurntSushi/toml"
)

func readTOMLConfig(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- trusted configuration path
	if err != nil {
		return nil, err
	}
	return decodeTOMLConfig(data)
}

func decodeTOMLConfig(data []byte) (map[string]any, error) {
	config := make(map[string]any)
	if _, err := toml.Decode(string(data), &config); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	return config, nil
}

func tomlTable(config map[string]any, path toml.Key) map[string]any {
	var value any = config
	for _, key := range path {
		table, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = table[key]
	}
	table, _ := value.(map[string]any)
	return table
}

func validateTOMLFields(config map[string]any, table toml.Key, fields map[string]any) error {
	actual := tomlTable(config, table)
	for key, want := range fields {
		encoded, err := toml.Marshal(map[string]any{key: want})
		if err != nil {
			return err
		}
		var normalized map[string]any
		if _, err := toml.Decode(string(encoded), &normalized); err != nil {
			return err
		}
		if !reflect.DeepEqual(actual[key], normalized[key]) {
			return fmt.Errorf("failed to update %s.%s", table.String(), key)
		}
	}
	return nil
}

func writeTOMLConfig(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWriteFile(path, data, mode)
}
