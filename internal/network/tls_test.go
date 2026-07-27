package network

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestTLSInspectParsesPEMAndDERWithoutReturningRawMaterial(t *testing.T) {
	pemBytes, derBytes := testCertificate(t)
	for name, content := range map[string][]byte{"certificate.pem": pemBytes, "certificate.der": derBytes} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			definition := testDefinition(t, t.TempDir(), nil)
			got, err := definition.Execute(context.Background(),
				invoke([]string{"tls", "inspect"}, []string{path}, nil))
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != protocol.StatusPass {
				t.Fatalf("result = %#v", got)
			}
			certificates := got.Data["certificates"].([]Certificate)
			if len(certificates) != 1 || certificates[0].Subject != "example.test" ||
				certificates[0].SHA256 == "" {
				t.Fatalf("certificates = %#v", certificates)
			}
			text := render(got.Data)
			if strings.Contains(text, "BEGIN CERTIFICATE") ||
				strings.Contains(text, string(derBytes)) {
				t.Fatalf("raw certificate material leaked: %q", text)
			}
		})
	}
}

func TestTLSInspectRejectsUnsafeFiles(t *testing.T) {
	pemBytes, _ := testCertificate(t)
	directory := t.TempDir()
	valid := filepath.Join(directory, "valid.pem")
	if err := os.WriteFile(valid, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "linked.pem")
	symlinkCreated := os.Symlink(valid, symlink) == nil
	parentSymlink := filepath.Join(t.TempDir(), "linked-directory")
	parentSymlinkCreated := os.Symlink(directory, parentSymlink) == nil
	oversized := filepath.Join(directory, "oversized.pem")
	if err := os.WriteFile(oversized, make([]byte, maxTLSFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	privateKey := filepath.Join(directory, "private.pem")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKey, append(pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER(t)}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})...), 0o600); err != nil {
		t.Fatal(err)
	}

	definition := testDefinition(t, t.TempDir(), nil)
	unsafePaths := []string{directory, oversized, privateKey, "-certificate.pem"}
	if symlinkCreated {
		unsafePaths = append(unsafePaths, symlink)
	}
	if parentSymlinkCreated {
		unsafePaths = append(unsafePaths, filepath.Join(parentSymlink, "valid.pem"))
	}
	for _, path := range unsafePaths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			_, err := definition.Execute(context.Background(),
				invoke([]string{"tls", "inspect"}, []string{path}, nil))
			if exitCode(err) != protocol.ExitArguments {
				t.Fatalf("%q: error = %v", path, err)
			}
		})
	}
}

func TestTLSInspectRejectsLeadingOptionInPlanAndExecute(t *testing.T) {
	definition := testDefinition(t, t.TempDir(), nil)
	invocation := invoke([]string{"tls", "inspect"}, []string{"-certificate.pem"}, nil)
	if _, err := definition.Plan(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
		t.Fatalf("plan error = %v", err)
	}
	if _, err := definition.Execute(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
		t.Fatalf("execute error = %v", err)
	}
}

func TestTLSInspectRequiresAbsoluteCleanPathInPlanAndExecute(t *testing.T) {
	directory := t.TempDir()
	unclean := filepath.Join(directory, "nested") + string(os.PathSeparator) +
		".." + string(os.PathSeparator) + "certificate.pem"
	for _, path := range []string{"certificate.pem", unclean} {
		t.Run(path, func(t *testing.T) {
			definition := testDefinition(t, t.TempDir(), nil)
			invocation := invoke([]string{"tls", "inspect"}, []string{path}, nil)
			if _, err := definition.Plan(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
				t.Fatalf("plan accepted %q: %v", path, err)
			}
			if _, err := definition.Execute(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
				t.Fatalf("execute accepted %q: %v", path, err)
			}
		})
	}
}

func TestTLSInspectWarnsForExpiredCertificate(t *testing.T) {
	der := createCertificate(t, fixedNow.Add(-48*time.Hour), fixedNow.Add(-24*time.Hour))
	path := filepath.Join(t.TempDir(), "expired.der")
	if err := os.WriteFile(path, der, 0o600); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t, t.TempDir(), nil)
	got, err := definition.Execute(context.Background(),
		invoke([]string{"tls", "inspect"}, []string{path}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusWarning || len(got.Checks) != 1 ||
		got.Checks[0].Status != protocol.StatusWarning {
		t.Fatalf("result = %#v", got)
	}
}

func testCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	der := certificateDER(t)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), der
}

func certificateDER(t *testing.T) []byte {
	t.Helper()
	return createCertificate(t, fixedNow.Add(-time.Hour), fixedNow.Add(24*time.Hour))
}

func createCertificate(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "example.test"},
		Issuer:       pkix.Name{CommonName: "example.test"},
		NotBefore:    notBefore, NotAfter: notAfter,
		DNSNames: []string{"example.test"},
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
