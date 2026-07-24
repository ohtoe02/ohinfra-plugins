package storage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Stats struct {
	Blocks    uint64
	Free      uint64
	Available uint64
	BlockSize uint64
	Files     uint64
	FilesFree uint64
}

type Filesystem struct {
	Source         string `json:"source"`
	MountPoint     string `json:"mount_point"`
	Type           string `json:"type"`
	TotalBytes     uint64 `json:"total_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	UsedPercent    int    `json:"used_percent"`
	TotalInodes    uint64 `json:"total_inodes"`
	UsedInodes     uint64 `json:"used_inodes"`
	Error          string `json:"error,omitempty"`
}

type Mount struct {
	Source     string
	MountPoint string
	Type       string
}

type Collector struct {
	MountInfoPath string
	StatFS        func(string) (Stats, error)
	ResolvePath   func(string) (string, error)
}

func DefaultCollector() Collector {
	return Collector{
		MountInfoPath: "/proc/self/mountinfo",
		StatFS:        statFS,
		ResolvePath:   filepath.EvalSymlinks,
	}
}

func (collector Collector) Collect(requestedPath string) ([]Filesystem, error) {
	path := collector.MountInfoPath
	if path == "" {
		path = "/proc/self/mountinfo"
	}
	content, err := os.ReadFile(path) // #nosec G304 -- production path is fixed; tests inject fixtures.
	if err != nil {
		return nil, fmt.Errorf("read mount table: %w", err)
	}
	mounts, err := ParseMountInfo(content)
	if err != nil {
		return nil, err
	}
	mounts = realMounts(mounts)
	if requestedPath != "" {
		resolver := collector.ResolvePath
		if resolver == nil {
			resolver = filepath.EvalSymlinks
		}
		resolved, err := resolver(requestedPath)
		if err != nil {
			return nil, fmt.Errorf("resolve path %s: %w", requestedPath, err)
		}
		mount, found := containingMount(resolved, mounts)
		if !found {
			return nil, fmt.Errorf("no mounted filesystem contains %s", resolved)
		}
		mounts = []Mount{mount}
	}

	stat := collector.StatFS
	if stat == nil {
		stat = statFS
	}
	filesystems := make([]Filesystem, 0, len(mounts))
	seen := map[string]struct{}{}
	for _, mount := range mounts {
		if _, duplicate := seen[mount.MountPoint]; duplicate {
			continue
		}
		seen[mount.MountPoint] = struct{}{}
		item := Filesystem{Source: mount.Source, MountPoint: mount.MountPoint, Type: mount.Type}
		stats, err := stat(mount.MountPoint)
		if err != nil {
			item.Error = err.Error()
			filesystems = append(filesystems, item)
			continue
		}
		usedBlocks := uint64(0)
		if stats.Blocks > stats.Free {
			usedBlocks = stats.Blocks - stats.Free
		}
		item.TotalBytes = stats.Blocks * stats.BlockSize
		item.UsedBytes = usedBlocks * stats.BlockSize
		item.AvailableBytes = stats.Available * stats.BlockSize
		denominator := usedBlocks + stats.Available
		if denominator > 0 {
			item.UsedPercent = boundedPercent(usedBlocks, denominator)
		}
		item.TotalInodes = stats.Files
		if stats.Files >= stats.FilesFree {
			item.UsedInodes = stats.Files - stats.FilesFree
		}
		filesystems = append(filesystems, item)
	}
	sort.Slice(filesystems, func(i, j int) bool {
		return filesystems[i].MountPoint < filesystems[j].MountPoint
	})
	return filesystems, nil
}

func boundedPercent(part, total uint64) int {
	if total == 0 {
		return 0
	}
	if part >= total {
		return 100
	}
	return int((float64(part) / float64(total)) * 100) // #nosec G115 -- ratio is bounded.
}

func ParseMountInfo(content []byte) ([]Mount, error) {
	lines := bytes.Split(content, []byte{'\n'})
	mounts := make([]Mount, 0, len(lines))
	for lineNumber, raw := range lines {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || separator+2 >= len(fields) {
			return nil, fmt.Errorf("invalid mountinfo line %d", lineNumber+1)
		}
		mounts = append(mounts, Mount{
			MountPoint: decodeMountField(fields[4]),
			Type:       fields[separator+1],
			Source:     decodeMountField(fields[separator+2]),
		})
	}
	if len(mounts) == 0 {
		return nil, errors.New("mount table contains no entries")
	}
	return mounts, nil
}

func realMounts(mounts []Mount) []Mount {
	output := make([]Mount, 0, len(mounts))
	for _, mount := range mounts {
		if _, pseudo := pseudoFilesystems[mount.Type]; !pseudo {
			output = append(output, mount)
		}
	}
	return output
}

func containingMount(path string, mounts []Mount) (Mount, bool) {
	path = filepath.Clean(path)
	var selected Mount
	found := false
	for _, mount := range mounts {
		point := filepath.Clean(mount.MountPoint)
		contains := point == string(filepath.Separator) ||
			path == point ||
			strings.HasPrefix(path, point+string(filepath.Separator))
		if contains && (!found || len(point) > len(selected.MountPoint)) {
			selected = mount
			found = true
		}
	}
	return selected, found
}

func decodeMountField(value string) string {
	return strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	).Replace(value)
}

var pseudoFilesystems = map[string]struct{}{
	"autofs": {}, "bpf": {}, "cgroup": {}, "cgroup2": {}, "configfs": {},
	"debugfs": {}, "devpts": {}, "devtmpfs": {}, "fusectl": {}, "hugetlbfs": {},
	"mqueue": {}, "proc": {}, "pstore": {}, "securityfs": {}, "sysfs": {},
	"tmpfs": {}, "tracefs": {},
}
