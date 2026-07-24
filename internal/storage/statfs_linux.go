//go:build linux

package storage

import "syscall"

func statFS(path string) (Stats, error) {
	var value syscall.Statfs_t
	if err := syscall.Statfs(path, &value); err != nil {
		return Stats{}, err
	}
	return Stats{
		Blocks: value.Blocks, Free: value.Bfree, Available: value.Bavail,
		BlockSize: uint64(value.Bsize), Files: value.Files, FilesFree: value.Ffree,
	}, nil
}
