package serversetup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSystemFileIdentityReaderHasExplicitCrossPlatformBehavior(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (systemFileIdentityReader{}).ReadFileIdentity(path, info)
	if runtime.GOOS == "linux" {
		if err != nil {
			t.Fatalf("ReadFileIdentity() error = %v", err)
		}
		return
	}
	if !errors.Is(err, ErrFileIdentityUnavailable) {
		t.Fatalf("ReadFileIdentity() error = %v, want ErrFileIdentityUnavailable", err)
	}
}
