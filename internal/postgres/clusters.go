package postgres

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const clusterOutputLimit int64 = 256 << 10

var (
	clusterIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	clusterStatus     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9,._-]{0,63}$`)
)

type Cluster struct {
	Version       string `json:"version"`
	Name          string `json:"name"`
	Port          int    `json:"port"`
	Status        string `json:"status"`
	Owner         string `json:"owner"`
	DataDirectory string `json:"data_directory"`
	LogFile       string `json:"log_file"`
}

func collectClusters(ctx context.Context, local probe.Local) ([]Cluster, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "pg_lsclusters", Arguments: []string{"--no-header"},
		StdoutLimit: clusterOutputLimit, StderrLimit: 16 << 10,
	})
	if err != nil {
		return nil, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
		return nil, errors.New("local PostgreSQL cluster inventory is unavailable")
	}
	clusters := make([]Cluster, 0)
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		port, parseErr := strconv.Atoi(fields[2])
		if parseErr != nil || port < 1 || port > 65535 {
			continue
		}
		if !clusterIdentifier.MatchString(fields[0]) ||
			!clusterIdentifier.MatchString(fields[1]) ||
			!clusterStatus.MatchString(fields[3]) ||
			!clusterIdentifier.MatchString(fields[4]) ||
			!validLocalPath(fields[5]) ||
			!validLocalPath(strings.Join(fields[6:], " ")) {
			continue
		}
		clusters = append(clusters, Cluster{
			Version: fields[0], Name: fields[1], Port: port,
			Status: fields[3], Owner: fields[4], DataDirectory: fields[5],
			LogFile: strings.Join(fields[6:], " "),
		})
	}
	sort.Slice(clusters, func(left, right int) bool {
		if clusters[left].Version == clusters[right].Version {
			return clusters[left].Name < clusters[right].Name
		}
		return clusters[left].Version < clusters[right].Version
	})
	return clusters, nil
}

func validLocalPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "://") ||
		strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return false
		}
	}
	return len(value) <= 4096
}

func executeClusters(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	clusters, err := collectClusters(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("pg_lsclusters local probe is unavailable")
	}
	return buildResult(options, "postgres clusters",
		map[string]any{"clusters": clusters},
		[]protocol.Check{{
			ID: "postgres.clusters", Status: protocol.StatusPass,
			Summary: "Local PostgreSQL clusters were inspected",
			Details: map[string]any{"count": len(clusters)},
		}}, nil), nil
}
