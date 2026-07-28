package serversetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type SetupTransaction interface {
	Apply(context.Context, protocol.Change) error
	Commit(context.Context) error
	Rollback(context.Context) error
}

type SetupTransactionFactory interface {
	Begin(root string, profile Profile) (SetupTransaction, error)
}

type SetupTransactionFactoryFunc func(string, Profile) (SetupTransaction, error)

func (function SetupTransactionFactoryFunc) Begin(
	root string,
	profile Profile,
) (SetupTransaction, error) {
	return function(root, profile)
}

type localSetupTransactionFactory struct {
	files    MutationFileAdapter
	commands MutationCommandAdapter
}

func (factory localSetupTransactionFactory) Begin(
	root string,
	profile Profile,
) (SetupTransaction, error) {
	files := factory.files
	if files == nil {
		files = defaultMutationFiles(root)
	}
	if factory.commands == nil {
		return nil, errors.New("mutation command adapter is unavailable")
	}
	return newLocalSetupTransaction(root, profile, files, factory.commands), nil
}

func defaultMutationFiles(root string) MutationFileAdapter {
	return &osMutationFiles{
		setIdentity: func(path, owner, group string) error {
			groupData, err := (probe.Local{Root: root}).Read("etc/group", maxAccountBytes)
			if err != nil {
				return err
			}
			passwdData, err := (probe.Local{Root: root}).Read("etc/passwd", maxAccountBytes)
			if err != nil {
				return err
			}
			groups, err := parseGroups(groupData)
			if err != nil {
				return err
			}
			users, err := parsePasswd(passwdData)
			if err != nil {
				return err
			}
			user, ownerExists := users[owner]
			gid, groupExists := groups[group]
			if !ownerExists || !groupExists {
				return errors.New("compiled owner or group is unavailable")
			}
			return os.Chown(path, user.id, gid)
		},
		copyMetadata: func(path string, source os.FileInfo) error {
			identity, err := fileIdentity(source)
			if err != nil {
				return err
			}
			return os.Chown(path, int(identity.UID), int(identity.GID))
		},
	}
}

type localSetupTransaction struct {
	root     string
	profile  Profile
	files    MutationFileAdapter
	commands MutationCommandAdapter
	backups  []fileBackup
	undo     []func(context.Context) error
}

func newLocalSetupTransaction(
	root string,
	profile Profile,
	files MutationFileAdapter,
	commands MutationCommandAdapter,
) *localSetupTransaction {
	return &localSetupTransaction{
		root: root, profile: cloneProfile(profile), files: files, commands: commands,
		backups: []fileBackup{}, undo: []func(context.Context) error{},
	}
}

func (transaction *localSetupTransaction) Apply(
	ctx context.Context,
	change protocol.Change,
) error {
	switch {
	case strings.HasPrefix(change.Object, "package:"):
		name := strings.TrimPrefix(change.Object, "package:")
		if !contains(transaction.profile.Packages, name) ||
			(change.Action != "install-cached" && change.Action != "upgrade-cached") {
			break
		}
		return transaction.applyUndo(transaction.commands.Package(
			ctx, name, change.Action == "upgrade-cached",
		))
	case strings.HasPrefix(change.Object, "group:") && change.Action == "create":
		name := strings.TrimPrefix(change.Object, "group:")
		for _, spec := range transaction.profile.Groups {
			if spec.Name == name {
				return transaction.applyUndo(transaction.commands.Group(ctx, spec))
			}
		}
	case strings.HasPrefix(change.Object, "user:") && change.Action == "create":
		name := strings.TrimPrefix(change.Object, "user:")
		for _, spec := range transaction.profile.Users {
			if spec.Name == name {
				return transaction.applyUndo(transaction.commands.User(ctx, spec))
			}
		}
	case strings.HasPrefix(change.Object, "directory:") && change.Action == "ensure":
		if transaction.files == nil {
			return errors.New("mutation file adapter is unavailable")
		}
		path := strings.TrimPrefix(change.Object, "directory:")
		for _, spec := range transaction.profile.Directories {
			if spec.Path == path {
				return transaction.applyUndo(transaction.files.EnsureDirectory(transaction.root, spec))
			}
		}
	case strings.HasPrefix(change.Object, "file:") && change.Action == "replace":
		if transaction.files == nil {
			return errors.New("mutation file adapter is unavailable")
		}
		path := strings.TrimPrefix(change.Object, "file:")
		for _, spec := range transaction.profile.ManagedFiles {
			if spec.Path != path {
				continue
			}
			backup, err := transaction.files.ReplaceManagedFile(transaction.root, spec)
			if backup.path != "" || backup.replacementInstalled {
				transaction.backups = append(transaction.backups, backup)
				transaction.undo = append(transaction.undo, func(context.Context) error {
					return transaction.files.RollbackManagedFile(backup)
				})
			}
			return err
		}
	case strings.HasPrefix(change.Object, "sysctl:") && change.Action == "set":
		key := strings.TrimPrefix(change.Object, "sysctl:")
		for _, spec := range transaction.profile.Sysctls {
			if spec.Key == key {
				return transaction.applyUndo(transaction.commands.Sysctl(ctx, spec))
			}
		}
	case strings.HasPrefix(change.Object, "unit:") && change.Action == "enable":
		unit := strings.TrimPrefix(change.Object, "unit:")
		if contains(transaction.profile.Units, unit) {
			return transaction.applyUndo(transaction.commands.Unit(ctx, unit))
		}
	}
	return fmt.Errorf("unsupported setup transaction change %s %s", change.Object, change.Action)
}

func (transaction *localSetupTransaction) Commit(context.Context) error {
	for _, backup := range transaction.backups {
		if err := transaction.files.CommitManagedFile(backup); err != nil {
			return err
		}
	}
	transaction.backups = nil
	transaction.undo = nil
	return nil
}

func (transaction *localSetupTransaction) Rollback(ctx context.Context) error {
	var failures []error
	for index := len(transaction.undo) - 1; index >= 0; index-- {
		if err := transaction.undo[index](ctx); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (transaction *localSetupTransaction) applyUndo(undo MutationUndo, err error) error {
	if err != nil {
		return err
	}
	if undo != nil {
		transaction.undo = append(transaction.undo, undo)
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
