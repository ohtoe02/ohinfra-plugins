//go:build !linux

package storage

import "errors"

func statFS(string) (Stats, error) {
	return Stats{}, errors.New("filesystem statistics are supported only on Linux")
}
