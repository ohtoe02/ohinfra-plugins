package serversetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestLocalTransactionReplacesManagedFileAtomicallyAndRollsBack(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	profile, err := ProfileByID("debian-12")
	if err != nil {
		t.Fatal(err)
	}
	spec := profile.ManagedFiles[0]
	target := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(spec.Path, "/")))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("original-state\n")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	files := &osMutationFiles{
		setIdentity:   func(string, string, string) error { return nil },
		copyMetadata:  func(string, os.FileInfo) error { return nil },
		syncDirectory: func(string) error { return nil },
	}
	transaction := newLocalSetupTransaction(root, profile, files, nil)

	err = transaction.Apply(context.Background(), protocol.Change{
		Object: "file:" + spec.Path, Action: "replace", Status: "planned",
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(spec.Content) {
		t.Fatalf("target = %q", got)
	}
	if len(transaction.backups) != 1 {
		t.Fatalf("backups = %#v", transaction.backups)
	}
	backup := transaction.backups[0].path
	if filepath.Dir(backup) != filepath.Dir(target) {
		t.Fatalf("backup %q is not beside target %q", backup, target)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup is unavailable before rollback: %v", err)
	}

	if err := transaction.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("rolled back target = %q, want %q", got, original)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("recovery backup was not preserved: %v", err)
	}
}

func TestLocalTransactionRollsBackEveryAtomicFileFailureStage(t *testing.T) {
	t.Parallel()

	stages := []string{
		"create-backup",
		"write-backup",
		"sync-backup",
		"chmod-backup",
		"identity-backup",
		"close-backup",
		"create-replacement",
		"write-replacement",
		"sync-replacement",
		"chmod-replacement",
		"identity-replacement",
		"close-replacement",
		"rename-replacement",
		"sync-directory",
	}
	for _, stage := range stages {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			profile, err := ProfileByID("debian-12")
			if err != nil {
				t.Fatal(err)
			}
			spec := profile.ManagedFiles[0]
			target := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(spec.Path, "/")))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			original := []byte("original-" + stage + "\n")
			if err := os.WriteFile(target, original, 0o600); err != nil {
				t.Fatal(err)
			}
			fired := false
			files := &osMutationFiles{
				setIdentity:   func(string, string, string) error { return nil },
				copyMetadata:  func(string, os.FileInfo) error { return nil },
				syncDirectory: func(string) error { return nil },
				failStage: func(current string) error {
					if current == stage && !fired {
						fired = true
						return errors.New("injected " + stage)
					}
					return nil
				},
			}
			transaction := newLocalSetupTransaction(root, profile, files, nil)
			err = transaction.Apply(context.Background(), protocol.Change{
				Object: "file:" + spec.Path, Action: "replace", Status: "planned",
			})
			if err == nil || !fired {
				t.Fatalf("Apply() error = %v, fired = %v", err, fired)
			}
			if rollbackErr := transaction.Rollback(context.Background()); rollbackErr != nil {
				t.Fatalf("Rollback() error = %v", rollbackErr)
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != string(original) {
				t.Fatalf("target after %s = %q, want %q", stage, got, original)
			}
			temporary, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".ohtools-write-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(temporary) != 0 {
				t.Fatalf("orphan temporary files after %s: %#v", stage, temporary)
			}
		})
	}
}

func TestLocalTransactionDispatchesOnlyCompiledChangesAndRollsBack(t *testing.T) {
	t.Parallel()

	profile, err := ProfileByID("debian-12")
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	files := &recordingMutationFiles{events: &events}
	commands := &recordingMutationCommands{events: &events}
	transaction := newLocalSetupTransaction(t.TempDir(), profile, files, commands)
	changes := []protocol.Change{
		{Object: "package:curl", Action: "install-cached"},
		{Object: "package:curl", Action: "upgrade-cached"},
		{Object: "group:ohtools", Action: "create"},
		{Object: "user:ohtools", Action: "create"},
		{Object: "directory:/etc/ohtools", Action: "ensure"},
		{Object: "file:/etc/ohtools/setup-state.yaml", Action: "replace"},
		{Object: "sysctl:fs.protected_hardlinks", Action: "set"},
		{Object: "unit:systemd-timesyncd.service", Action: "enable"},
	}
	for _, change := range changes {
		if err := transaction.Apply(context.Background(), change); err != nil {
			t.Fatalf("Apply(%#v) error = %v", change, err)
		}
	}
	if err := transaction.Apply(context.Background(), protocol.Change{
		Object: "package:attacker", Action: "install-cached",
	}); err == nil {
		t.Fatal("transaction accepted a package outside the compiled profile")
	}
	if err := transaction.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package:curl:false",
		"package:curl:true",
		"group:ohtools",
		"user:ohtools",
		"directory:/etc/ohtools",
		"file:/etc/ohtools/setup-state.yaml",
		"sysctl:fs.protected_hardlinks",
		"unit:systemd-timesyncd.service",
		"rollback:file",
	} {
		if !containsString(events, want) {
			t.Fatalf("event %q missing from %#v", want, events)
		}
	}
}

type recordingMutationFiles struct {
	events *[]string
}

func (files *recordingMutationFiles) ReplaceManagedFile(
	_ string,
	spec ManagedFileSpec,
) (fileBackup, error) {
	*files.events = append(*files.events, "file:"+spec.Path)
	return fileBackup{target: spec.Path, replacementInstalled: true}, nil
}

func (files *recordingMutationFiles) RollbackManagedFile(fileBackup) error {
	*files.events = append(*files.events, "rollback:file")
	return nil
}

func (files *recordingMutationFiles) CommitManagedFile(fileBackup) error {
	*files.events = append(*files.events, "commit:file")
	return nil
}

func (files *recordingMutationFiles) EnsureDirectory(
	_ string,
	spec DirectorySpec,
) (MutationUndo, error) {
	*files.events = append(*files.events, "directory:"+spec.Path)
	return func(context.Context) error {
		*files.events = append(*files.events, "undo:directory:"+spec.Path)
		return nil
	}, nil
}

type recordingMutationCommands struct {
	events *[]string
}

func (commands *recordingMutationCommands) Package(
	_ context.Context,
	name string,
	upgrade bool,
) (MutationUndo, error) {
	*commands.events = append(*commands.events, "package:"+name+":"+strconv.FormatBool(upgrade))
	return recordingUndo(commands.events, "package:"+name), nil
}

func (commands *recordingMutationCommands) Group(
	_ context.Context,
	spec GroupSpec,
) (MutationUndo, error) {
	*commands.events = append(*commands.events, "group:"+spec.Name)
	return recordingUndo(commands.events, "group:"+spec.Name), nil
}

func (commands *recordingMutationCommands) User(
	_ context.Context,
	spec UserSpec,
) (MutationUndo, error) {
	*commands.events = append(*commands.events, "user:"+spec.Name)
	return recordingUndo(commands.events, "user:"+spec.Name), nil
}

func (commands *recordingMutationCommands) Sysctl(
	_ context.Context,
	spec SysctlSpec,
) (MutationUndo, error) {
	*commands.events = append(*commands.events, "sysctl:"+spec.Key)
	return recordingUndo(commands.events, "sysctl:"+spec.Key), nil
}

func (commands *recordingMutationCommands) Unit(
	_ context.Context,
	unit string,
) (MutationUndo, error) {
	*commands.events = append(*commands.events, "unit:"+unit)
	return recordingUndo(commands.events, "unit:"+unit), nil
}

func recordingUndo(events *[]string, value string) MutationUndo {
	return func(context.Context) error {
		*events = append(*events, "undo:"+value)
		return nil
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
