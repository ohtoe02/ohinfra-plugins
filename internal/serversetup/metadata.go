package serversetup

import (
	"errors"
	"os"
)

var ErrFileIdentityUnavailable = errors.New("file ownership metadata is unavailable")

type FileIdentity struct {
	UID uint32
	GID uint32
}

type FileIdentityReader interface {
	ReadFileIdentity(path string, info os.FileInfo) (FileIdentity, error)
}

type FileIdentityReaderFunc func(string, os.FileInfo) (FileIdentity, error)

func (function FileIdentityReaderFunc) ReadFileIdentity(
	path string,
	info os.FileInfo,
) (FileIdentity, error) {
	return function(path, info)
}

type systemFileIdentityReader struct{}

func (systemFileIdentityReader) ReadFileIdentity(
	_ string,
	info os.FileInfo,
) (FileIdentity, error) {
	return fileIdentity(info)
}

func matchesCompiledIdentity(
	reader FileIdentityReader,
	path string,
	info os.FileInfo,
	owner string,
	group string,
	accounts accountDatabase,
) (bool, error) {
	expectedOwner, ownerExists := accounts.users[owner]
	expectedGroup, groupExists := accounts.groups[group]
	if !ownerExists || !groupExists {
		return false, nil
	}
	actual, err := reader.ReadFileIdentity(path, info)
	if err != nil {
		return false, err
	}
	return actual.UID == uint32(expectedOwner.id) &&
		actual.GID == uint32(expectedGroup), nil
}
