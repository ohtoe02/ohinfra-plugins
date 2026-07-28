package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	configFileLimit = 1 << 20
	configDirLimit  = 64
)

var safeConfigComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var publicSettings = map[string]struct{}{
	"effective_cache_size": {},
	"hot_standby":          {},
	"listen_addresses":     {},
	"log_destination":      {},
	"logging_collector":    {},
	"maintenance_work_mem": {},
	"max_connections":      {},
	"max_wal_senders":      {},
	"port":                 {},
	"shared_buffers":       {},
	"timezone":             {},
	"wal_level":            {},
	"work_mem":             {},
}

type ConfigFile struct {
	Path            string            `json:"path"`
	Size            int               `json:"size"`
	NonCommentLines int               `json:"non_comment_lines"`
	Settings        map[string]string `json:"settings"`
}

func executeConfig(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	files, err := collectConfig(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("PostgreSQL local configuration is unavailable")
	}
	if len(files) == 0 {
		return protocol.Result{}, dependencyFailure("PostgreSQL local configuration is unavailable")
	}
	return buildResult(options, "postgres config",
		map[string]any{"files": files},
		[]protocol.Check{{
			ID: "postgres.config", Status: protocol.StatusPass,
			Summary: "Local PostgreSQL configuration metadata was inspected",
			Details: map[string]any{"file_count": len(files)},
		}}, nil), nil
}

func collectConfig(ctx context.Context, local probe.Local) ([]ConfigFile, error) {
	const base = "etc/postgresql"
	exists, err := local.Exists(filepath.FromSlash(base))
	if err != nil {
		return nil, err
	}
	if !exists {
		return []ConfigFile{}, nil
	}
	versions, err := boundedDirectories(filepath.Join(local.Root, filepath.FromSlash(base)))
	if err != nil {
		return nil, err
	}
	files := []ConfigFile{}
	for _, version := range versions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !safeConfigComponent.MatchString(version) {
			continue
		}
		versionRelative := filepath.Join(filepath.FromSlash(base), version)
		if _, existsErr := local.Exists(versionRelative); existsErr != nil {
			return nil, existsErr
		}
		clusters, dirErr := boundedDirectories(filepath.Join(local.Root, versionRelative))
		if dirErr != nil {
			return nil, dirErr
		}
		for _, cluster := range clusters {
			if !safeConfigComponent.MatchString(cluster) {
				continue
			}
			clusterRelative := filepath.Join(versionRelative, cluster)
			if _, existsErr := local.Exists(clusterRelative); existsErr != nil {
				return nil, existsErr
			}
			for _, name := range []string{
				"pg_hba.conf", "pg_ident.conf", "postgresql.auto.conf", "postgresql.conf",
			} {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				relative := filepath.Join(clusterRelative, name)
				present, existsErr := local.Exists(relative)
				if existsErr != nil {
					return nil, existsErr
				}
				if !present {
					continue
				}
				content, readErr := local.Read(relative, configFileLimit)
				if readErr != nil {
					return nil, readErr
				}
				files = append(files, parseConfigFile(filepath.ToSlash(relative), content))
			}
		}
	}
	sort.Slice(files, func(left, right int) bool {
		return files[left].Path < files[right].Path
	})
	return files, nil
}

func boundedDirectories(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	if len(entries) > configDirLimit {
		return nil, errors.New("PostgreSQL configuration inventory exceeds limit")
	}
	output := []string{}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 && entry.IsDir() {
			output = append(output, entry.Name())
		}
	}
	sort.Strings(output)
	return output, nil
}

func parseConfigFile(path string, content []byte) ConfigFile {
	settings := map[string]string{}
	nonCommentLines := 0
	if strings.HasSuffix(path, "postgresql.conf") ||
		strings.HasSuffix(path, "postgresql.auto.conf") {
		for _, raw := range strings.Split(string(content), "\n") {
			line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
			if line == "" {
				continue
			}
			nonCommentLines++
			key, value, ok := strings.Cut(line, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			if !ok {
				continue
			}
			if _, allowed := publicSettings[key]; !allowed {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			if len(value) > 1024 {
				continue
			}
			if strings.Contains(value, "://") {
				value = "[redacted-uri]"
			}
			settings[key] = value
		}
	} else {
		for _, raw := range strings.Split(string(content), "\n") {
			line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
			if line != "" {
				nonCommentLines++
			}
		}
	}
	return ConfigFile{
		Path: path, Size: len(content), NonCommentLines: nonCommentLines, Settings: settings,
	}
}
