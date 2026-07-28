package serversetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type fileBackup struct {
	target               string
	path                 string
	hadOriginal          bool
	replacementInstalled bool
}

type MutationFileAdapter interface {
	ReplaceManagedFile(root string, spec ManagedFileSpec) (fileBackup, error)
	RollbackManagedFile(fileBackup) error
	CommitManagedFile(fileBackup) error
	EnsureDirectory(root string, spec DirectorySpec) (MutationUndo, error)
}

func (files *osMutationFiles) EnsureDirectory(
	root string,
	spec DirectorySpec,
) (MutationUndo, error) {
	target, err := rootedPath(root, spec.Path)
	if err != nil {
		return nil, err
	}
	parentSpec := filepath.ToSlash(filepath.Dir(spec.Path))
	if _, _, err := (localPathConfinement{}).Inspect(root, parentSpec); err != nil {
		return nil, fmt.Errorf("inspect directory parent: %w", err)
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(target, os.FileMode(spec.Mode)); err != nil {
			return nil, err
		}
		if err := files.applyCompiledIdentity(target, spec.Owner, spec.Group); err != nil {
			_ = os.Remove(target)
			return nil, err
		}
		if err := files.syncDir(filepath.Dir(target)); err != nil {
			_ = os.Remove(target)
			return nil, err
		}
		return func(context.Context) error {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return files.syncDir(filepath.Dir(target))
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("directory target must be a non-symlink directory")
	}
	oldMode := info.Mode().Perm()
	if err := os.Chmod(target, os.FileMode(spec.Mode)); err != nil {
		return nil, err
	}
	if err := files.applyCompiledIdentity(target, spec.Owner, spec.Group); err != nil {
		_ = os.Chmod(target, oldMode)
		return nil, err
	}
	return func(context.Context) error {
		if err := os.Chmod(target, oldMode); err != nil {
			return err
		}
		return files.applyCopiedMetadata(target, info)
	}, nil
}

type osMutationFiles struct {
	setIdentity   func(path string, owner string, group string) error
	copyMetadata  func(path string, source os.FileInfo) error
	syncDirectory func(path string) error
	failStage     func(stage string) error
}

func (files *osMutationFiles) ReplaceManagedFile(
	root string,
	spec ManagedFileSpec,
) (fileBackup, error) {
	target, err := rootedPath(root, spec.Path)
	if err != nil {
		return fileBackup{}, err
	}
	parent := filepath.Dir(target)
	parentSpec := filepath.ToSlash(filepath.Dir(spec.Path))
	if _, _, err := (localPathConfinement{}).Inspect(root, parentSpec); err != nil {
		return fileBackup{}, fmt.Errorf("inspect managed-file parent: %w", err)
	}

	backup := fileBackup{target: target}
	info, err := os.Lstat(target)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fileBackup{}, errors.New("managed-file target must be a regular non-symlink file")
		}
		content, readErr := os.ReadFile(target) // #nosec G304 -- target is a confined compiled path.
		if readErr != nil {
			return fileBackup{}, readErr
		}
		backup.path, err = files.createDurableTemp(
			parent, ".ohtools-backup-*", content, info.Mode().Perm(),
			func(path string) error { return files.applyCopiedMetadata(path, info) },
			"backup",
		)
		if err != nil {
			return fileBackup{}, err
		}
		backup.hadOriginal = true
	case errors.Is(err, os.ErrNotExist):
	default:
		return fileBackup{}, err
	}

	temp, err := files.createDurableTemp(
		parent, ".ohtools-write-*", spec.Content, os.FileMode(spec.Mode),
		func(path string) error { return files.applyCompiledIdentity(path, spec.Owner, spec.Group) },
		"replacement",
	)
	if err != nil {
		return backup, err
	}
	if err := files.stage("rename-replacement"); err != nil {
		_ = os.Remove(temp)
		return backup, err
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return backup, err
	}
	backup.replacementInstalled = true
	if err := files.syncDir(parent); err != nil {
		return backup, err
	}
	return backup, nil
}

func (files *osMutationFiles) RollbackManagedFile(backup fileBackup) error {
	parent := filepath.Dir(backup.target)
	if !backup.hadOriginal {
		if err := os.Remove(backup.target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return files.syncDir(parent)
	}
	content, err := os.ReadFile(backup.path) // #nosec G304 -- backup path is created by this adapter.
	if err != nil {
		return err
	}
	info, err := os.Lstat(backup.path)
	if err != nil {
		return err
	}
	temp, err := files.createDurableTemp(
		parent, ".ohtools-restore-*", content, info.Mode().Perm(),
		func(path string) error { return files.applyCopiedMetadata(path, info) },
		"restore",
	)
	if err != nil {
		return err
	}
	if err := files.stage("rename-restore"); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, backup.target); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return files.syncDir(parent)
}

func (files *osMutationFiles) CommitManagedFile(backup fileBackup) error {
	if backup.path == "" {
		return nil
	}
	if err := os.Remove(backup.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return files.syncDir(filepath.Dir(backup.target))
}

func (files *osMutationFiles) createDurableTemp(
	directory string,
	pattern string,
	content []byte,
	mode os.FileMode,
	metadata func(string) error,
	stagePrefix string,
) (path string, resultErr error) {
	if err := files.stage("create-" + stagePrefix); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	path = file.Name()
	temporaryPath := path
	defer func() {
		if resultErr != nil {
			_ = file.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := files.stage("write-" + stagePrefix); err != nil {
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		return "", err
	}
	if err := files.stage("sync-" + stagePrefix); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := files.stage("chmod-" + stagePrefix); err != nil {
		return "", err
	}
	if err := file.Chmod(mode); err != nil {
		return "", err
	}
	if err := files.stage("identity-" + stagePrefix); err != nil {
		return "", err
	}
	if err := metadata(path); err != nil {
		return "", err
	}
	if err := files.stage("close-" + stagePrefix); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func (files *osMutationFiles) applyCompiledIdentity(path, owner, group string) error {
	if files.setIdentity != nil {
		return files.setIdentity(path, owner, group)
	}
	return errors.New("compiled identity setter is unavailable")
}

func (files *osMutationFiles) applyCopiedMetadata(path string, source os.FileInfo) error {
	if files.copyMetadata != nil {
		return files.copyMetadata(path, source)
	}
	return errors.New("metadata copy adapter is unavailable")
}

func (files *osMutationFiles) syncDir(path string) error {
	if err := files.stage("sync-directory"); err != nil {
		return err
	}
	if files.syncDirectory != nil {
		return files.syncDirectory(path)
	}
	directory, err := os.Open(path) // #nosec G304 -- path is a confined compiled directory.
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (files *osMutationFiles) stage(name string) error {
	if files.failStage == nil {
		return nil
	}
	return files.failStage(strings.TrimSpace(name))
}
