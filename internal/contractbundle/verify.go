package contractbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/strictjson"
)

const sourceRepository = "ohtoe02/ohtools-plugin-catalog"

var (
	lowerDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitHash  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type Lock struct {
	SchemaVersion    string `json:"schema_version"`
	SourceRepository string `json:"source_repository"`
	SourceCommit     string `json:"source_commit"`
	BundleSHA256     string `json:"bundle_sha256"`
}

func Verify(bundleRoot, lockPath string) error {
	lockJSON, err := readBoundedRegular(lockPath, 8<<10)
	if err != nil {
		return fmt.Errorf("read contract lock: %w", err)
	}
	var lock Lock
	if err := strictjson.Decode(lockJSON, &lock); err != nil {
		return fmt.Errorf("decode contract lock: %w", err)
	}
	if lock.SchemaVersion != "1" || lock.SourceRepository != sourceRepository ||
		!commitHash.MatchString(lock.SourceCommit) || !lowerDigest.MatchString(lock.BundleSHA256) {
		return errors.New("contract lock has invalid provenance")
	}

	sumsPath := filepath.Join(bundleRoot, "SHA256SUMS")
	sums, err := readBoundedRegular(sumsPath, 1<<20)
	if err != nil {
		return fmt.Errorf("read contract checksums: %w", err)
	}
	bundleDigest := sha256.Sum256(sums)
	if hex.EncodeToString(bundleDigest[:]) != lock.BundleSHA256 {
		return errors.New("contract checksum bundle does not match its lock")
	}

	listed, err := parseSums(sums)
	if err != nil {
		return err
	}
	actual := map[string]struct{}{}
	err = filepath.WalkDir(bundleRoot, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == bundleRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("contract bundle contains symlink %q", filePath)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("contract bundle contains non-regular file %q", filePath)
		}
		relative, err := filepath.Rel(bundleRoot, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "SHA256SUMS" {
			return nil
		}
		actual[relative] = struct{}{}
		expected, found := listed[relative]
		if !found {
			return fmt.Errorf("contract file %q is not listed in SHA256SUMS", relative)
		}
		digest, err := hashRegular(filePath)
		if err != nil {
			return err
		}
		if digest != expected {
			return fmt.Errorf("contract file %q does not match SHA256SUMS", relative)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(actual) != len(listed) {
		return errors.New("SHA256SUMS references a missing contract file")
	}
	return nil
}

func parseSums(encoded []byte) (map[string]string, error) {
	if len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
		return nil, errors.New("SHA256SUMS must be non-empty and newline-terminated")
	}
	listed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n") {
		if len(line) < 67 || line[64:66] != "  " {
			return nil, fmt.Errorf("invalid SHA256SUMS line %q", line)
		}
		digest, relative := line[:64], line[66:]
		if !lowerDigest.MatchString(digest) || relative == "" || strings.Contains(relative, `\`) ||
			strings.HasPrefix(relative, "/") || path.Clean(relative) != relative ||
			relative == "SHA256SUMS" {
			return nil, fmt.Errorf("unsafe SHA256SUMS entry %q", line)
		}
		if _, duplicate := listed[relative]; duplicate {
			return nil, fmt.Errorf("duplicate SHA256SUMS entry %q", relative)
		}
		listed[relative] = digest
	}
	return listed, nil
}

func readBoundedRegular(filePath string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("file must be a bounded non-empty regular non-symlink file")
	}
	return os.ReadFile(filePath)
}

func hashRegular(filePath string) (string, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("contract file must be regular and must not be a symlink")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
