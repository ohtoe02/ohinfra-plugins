package network

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

const maxTLSFileSize int64 = 1 << 20

type Certificate struct {
	Subject      string   `json:"subject"`
	Issuer       string   `json:"issuer"`
	SerialNumber string   `json:"serial_number"`
	NotBefore    string   `json:"not_before"`
	NotAfter     string   `json:"not_after"`
	DNSNames     []string `json:"dns_names"`
	IsCA         bool     `json:"is_ca"`
	SHA256       string   `json:"sha256"`
}

func executeTLS(options Options, path string) (protocol.Result, error) {
	certificates, err := inspectTLSFile(path)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
	}
	checks := make([]protocol.Check, 0, len(certificates))
	now := options.Now()
	for index, certificate := range certificates {
		status := protocol.StatusPass
		summary := "Certificate is currently valid"
		notBefore, _ := time.Parse(time.RFC3339Nano, certificate.NotBefore)
		notAfter, _ := time.Parse(time.RFC3339Nano, certificate.NotAfter)
		if now.Before(notBefore) {
			status = protocol.StatusWarning
			summary = "Certificate is not valid yet"
		} else if !now.Before(notAfter) {
			status = protocol.StatusWarning
			summary = "Certificate is expired"
		}
		checks = append(checks, protocol.Check{
			ID:     fmt.Sprintf("tls.certificate.%03d", index+1),
			Status: status, Summary: summary,
			Details: map[string]any{"sha256": certificate.SHA256},
		})
	}
	return buildResult(options, "tls inspect",
		map[string]any{"certificates": certificates}, checks, nil), nil
}

func inspectTLSFile(path string) ([]Certificate, error) {
	if err := validateTLSPathArgument(path); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid TLS certificate path")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, errors.New("TLS certificate file is unavailable")
	}
	if filepath.Clean(resolved) != filepath.Clean(absolute) {
		return nil, errors.New("TLS certificate path must not contain symlinks")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, errors.New("TLS certificate file is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("TLS certificate path must be a regular non-symlink file")
	}
	if info.Size() > maxTLSFileSize {
		return nil, errors.New("TLS certificate file exceeds the size limit")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, errors.New("TLS certificate file is unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("TLS certificate file changed during inspection")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTLSFileSize+1))
	if err != nil || int64(len(data)) > maxTLSFileSize {
		return nil, errors.New("TLS certificate file exceeds the size limit")
	}
	certificates, err := parseCertificates(data)
	if err != nil {
		return nil, err
	}
	return certificates, nil
}

func validateTLSPathArgument(path string) error {
	if path == "" || strings.TrimSpace(path) != path || strings.HasPrefix(path, "-") ||
		strings.ContainsAny(path, "\x00\r\n") || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path {
		return errors.New("invalid TLS certificate path")
	}
	return nil
}

func parseCertificates(data []byte) ([]Certificate, error) {
	var parsed []*x509.Certificate
	remaining := data
	sawPEM := false
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		sawPEM = true
		remaining = rest
		if strings.Contains(strings.ToUpper(block.Type), "PRIVATE KEY") {
			return nil, errors.New("TLS input must not contain private key material")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("TLS input contains an invalid certificate")
		}
		parsed = append(parsed, certificate)
	}
	if sawPEM {
		if len(strings.TrimSpace(string(remaining))) != 0 {
			return nil, errors.New("TLS PEM input contains trailing non-PEM data")
		}
		if len(parsed) == 0 {
			return nil, errors.New("TLS PEM input contains no certificates")
		}
	} else {
		certificate, err := x509.ParseCertificate(data)
		if err != nil {
			return nil, errors.New("TLS input is not a valid PEM or DER certificate")
		}
		parsed = []*x509.Certificate{certificate}
	}

	output := make([]Certificate, 0, len(parsed))
	for _, certificate := range parsed {
		digest := sha256.Sum256(certificate.Raw)
		subject := certificate.Subject.CommonName
		if subject == "" {
			subject = certificate.Subject.String()
		}
		issuer := certificate.Issuer.CommonName
		if issuer == "" {
			issuer = certificate.Issuer.String()
		}
		dnsNames := append([]string(nil), certificate.DNSNames...)
		for index := range dnsNames {
			dnsNames[index] = redact.String(dnsNames[index])
		}
		sort.Strings(dnsNames)
		output = append(output, Certificate{
			Subject:      redact.String(subject),
			Issuer:       redact.String(issuer),
			SerialNumber: certificate.SerialNumber.Text(16),
			NotBefore:    certificate.NotBefore.UTC().Format(time.RFC3339Nano),
			NotAfter:     certificate.NotAfter.UTC().Format(time.RFC3339Nano),
			DNSNames:     dnsNames, IsCA: certificate.IsCA,
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	return output, nil
}
