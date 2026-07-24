package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

func Load[T any](path string, defaults T) (T, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err != nil {
		return defaults, fmt.Errorf("inspect plugin config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return defaults, errors.New("plugin config must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return defaults, errors.New("plugin config must be a regular file")
	}
	if err := validateTrustedFile(path, info); err != nil {
		return defaults, err
	}
	encoded, err := os.ReadFile(path) // #nosec G304 -- the path is fixed by the plugin.
	if err != nil {
		return defaults, fmt.Errorf("read plugin config: %w", err)
	}
	output := defaults
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&output); err != nil {
		return defaults, fmt.Errorf("decode plugin config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return defaults, errors.New("plugin config must contain one YAML document")
		}
		return defaults, fmt.Errorf("decode plugin config: %w", err)
	}
	return output, nil
}
