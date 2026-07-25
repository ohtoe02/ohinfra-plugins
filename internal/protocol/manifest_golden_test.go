package protocol_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	dockerplugin "github.com/ohtoe02/ohtools-plugins/internal/docker"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/storage"
	"github.com/ohtoe02/ohtools-plugins/internal/system"
	"github.com/ohtoe02/ohtools-plugins/internal/systemd"
)

func TestFirstPartyManifestCompatibility(t *testing.T) {
	t.Parallel()

	definitions := map[string]protocol.Definition{
		"system-base":  system.NewDefinition(system.Options{Version: "1.0.1"}),
		"storage-base": storage.NewDefinition(storage.Options{Version: "1.0.1"}),
		"systemd-base": systemd.NewDefinition(systemd.Options{Version: "1.0.1"}),
		"docker-base":  dockerplugin.NewDefinition(dockerplugin.Options{Version: "1.0.1"}),
	}
	for name, definition := range definitions {
		name, definition := name, definition
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(definition.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "manifests", name+".json")
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
			if string(got) != string(want) {
				t.Fatalf("manifest changed; got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
