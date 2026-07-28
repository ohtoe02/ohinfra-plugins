package platform

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const maxReleaseSize = 64 * 1024

// Info is the normalized operating-system identity used by first-party plugins.
type Info struct {
	ID        string
	VersionID string
	Supported bool
}

// Detect reads and validates etc/os-release below root.
func Detect(root string) (Info, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return Info{}, errors.New("platform root must be an absolute clean path")
	}

	path := filepath.Join(root, "etc", "os-release")
	if err := validateReleasePath(root, path); err != nil {
		return Info{}, err
	}

	file, err := os.Open(path)
	if err != nil {
		return Info{}, fmt.Errorf("open os-release: %w", err)
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxReleaseSize+1))
	if err != nil {
		return Info{}, fmt.Errorf("read os-release: %w", err)
	}
	if len(content) > maxReleaseSize {
		return Info{}, fmt.Errorf("os-release exceeds %d bytes", maxReleaseSize)
	}

	values, err := parseRelease(content)
	if err != nil {
		return Info{}, err
	}
	id, idOK := values["ID"]
	versionID, versionOK := values["VERSION_ID"]
	if !idOK || id == "" {
		return Info{}, errors.New("os-release is missing ID")
	}
	if !versionOK || versionID == "" {
		return Info{}, errors.New("os-release is missing VERSION_ID")
	}

	return Info{
		ID:        id,
		VersionID: versionID,
		Supported: supported(id, versionID),
	}, nil
}

func validateReleasePath(root, releasePath string) error {
	hasSymlink := false
	for _, path := range []string{root, filepath.Join(root, "etc"), releasePath} {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			hasSymlink = true
			if !isFilesystemRoot(root) {
				return fmt.Errorf("%s must not be a symlink", filepath.Base(path))
			}
		}
	}

	resolvedPath, err := filepath.EvalSymlinks(releasePath)
	if err != nil {
		return fmt.Errorf("resolve os-release: %w", err)
	}
	resolvedInfo, err := os.Stat(resolvedPath)
	if err != nil {
		return fmt.Errorf("inspect resolved os-release: %w", err)
	}
	return validateReleaseTarget(root, resolvedPath, hasSymlink, resolvedInfo.Mode())
}

func validateReleaseTarget(root, resolvedPath string, hasSymlink bool, mode os.FileMode) error {
	if hasSymlink && !isFilesystemRoot(root) {
		return errors.New("fixture os-release must not use symlinks")
	}
	relative, err := filepath.Rel(root, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("resolved os-release escapes platform root")
	}
	if !mode.IsRegular() {
		return errors.New("resolved os-release must be a regular file")
	}
	return nil
}

func isFilesystemRoot(root string) bool {
	volume := filepath.VolumeName(root)
	return filepath.Clean(root) == filepath.Clean(volume+string(filepath.Separator))
}

func parseRelease(content []byte) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 1024), maxReleaseSize)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		key, raw, ok := strings.Cut(line, "=")
		if !ok || !validKey(key) {
			return nil, fmt.Errorf("invalid os-release line %d", lineNumber)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate os-release key %q", key)
		}
		value, err := parseValue(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid os-release value for %q: %w", key, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan os-release: %w", err)
	}
	return values, nil
}

func validKey(key string) bool {
	if key == "" || (key[0] != '_' && (key[0] < 'A' || key[0] > 'Z')) {
		return false
	}
	for index := 1; index < len(key); index++ {
		char := key[index]
		if char != '_' && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func parseValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '\'':
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", errors.New("unterminated single quote")
		}
		value := raw[1 : len(raw)-1]
		if strings.ContainsRune(value, '\'') {
			return "", errors.New("unexpected single quote")
		}
		return value, nil
	case '"':
		if len(raw) < 2 || raw[len(raw)-1] != '"' {
			return "", errors.New("unterminated double quote")
		}
		return unescapeDoubleQuoted(raw[1 : len(raw)-1])
	default:
		for _, char := range raw {
			if unicode.IsSpace(char) || char == '\'' || char == '"' || char == '\\' || unicode.IsControl(char) {
				return "", errors.New("invalid unquoted value")
			}
		}
		return raw, nil
	}
}

func unescapeDoubleQuoted(raw string) (string, error) {
	var value strings.Builder
	value.Grow(len(raw))
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			value.WriteByte(raw[index])
			continue
		}
		index++
		if index >= len(raw) {
			return "", errors.New("dangling escape")
		}
		switch raw[index] {
		case '"', '\\', '$', '`':
			value.WriteByte(raw[index])
		default:
			value.WriteByte('\\')
			value.WriteByte(raw[index])
		}
	}
	return value.String(), nil
}

func supported(id, versionID string) bool {
	switch id {
	case "debian":
		switch versionID {
		case "10", "11", "12", "13":
			return true
		}
	case "ubuntu":
		switch versionID {
		case "20.04", "22.04", "24.04":
			return true
		}
	}
	return false
}
