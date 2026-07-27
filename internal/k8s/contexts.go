package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"go.yaml.in/yaml/v3"
)

const (
	kubeconfigLimit  int64 = 512 * 1024
	maxKubeEntries         = 256
	maxMetadataBytes       = 512
)

var kubeconfigPaths = []string{
	"etc/kubernetes/admin.conf",
	"etc/kubernetes/kubelet.conf",
	"etc/kubernetes/controller-manager.conf",
	"etc/kubernetes/scheduler.conf",
}

type ContextMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type KubeconfigMetadata struct {
	Path           string            `json:"path"`
	CurrentContext string            `json:"current_context,omitempty"`
	Contexts       []ContextMetadata `json:"contexts"`
	ClusterCount   int               `json:"cluster_count"`
	UserCount      int               `json:"user_count"`
}

func executeContexts(ctx context.Context, options Options) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	local := probe.Local{Root: options.Root}
	configs := []KubeconfigMetadata{}
	for _, path := range kubeconfigPaths {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		encoded, err := local.Read(path, kubeconfigLimit)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return protocol.Result{}, configurationFailure(
				"local kubeconfig is not a safe bounded regular file",
			)
		}
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		metadata, err := parseKubeconfig(ctx, path, encoded)
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return protocol.Result{}, fatal
			}
			return protocol.Result{}, configurationFailure(
				"local kubeconfig metadata is invalid",
			)
		}
		configs = append(configs, metadata)
	}
	status := protocol.StatusPass
	summary := fmt.Sprintf("Collected metadata from %d local kubeconfig files", len(configs))
	if len(configs) == 0 {
		status = protocol.StatusInfo
		summary = "No local Kubernetes kubeconfig files were found"
	}
	return buildResult(options, "k8s contexts", map[string]any{
		"kubeconfigs": configs,
	}, []protocol.Check{{
		ID: "k8s.contexts", Status: status, Summary: summary,
	}}, nil), nil
}

func configurationFailure(message string) error {
	return protocol.ExitError{
		Code: protocol.ExitConfiguration,
		Err:  errors.New(message),
	}
}

func parseKubeconfig(
	ctx context.Context,
	path string,
	encoded []byte,
) (KubeconfigMetadata, error) {
	if err := ctx.Err(); err != nil {
		return KubeconfigMetadata{}, err
	}
	if !utf8.Valid(encoded) {
		return KubeconfigMetadata{}, errors.New("kubeconfig is not valid UTF-8")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&document); err != nil {
		return KubeconfigMetadata{}, err
	}
	if err := ctx.Err(); err != nil {
		return KubeconfigMetadata{}, err
	}
	if err := rejectAliases(&document); err != nil {
		return KubeconfigMetadata{}, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return KubeconfigMetadata{}, errors.New("kubeconfig contains multiple YAML documents")
		}
		return KubeconfigMetadata{}, err
	}

	var config struct {
		APIVersion     string `yaml:"apiVersion"`
		Kind           string `yaml:"kind"`
		CurrentContext string `yaml:"current-context"`
		Clusters       []struct {
			Name string `yaml:"name"`
		} `yaml:"clusters"`
		Users []struct {
			Name string `yaml:"name"`
		} `yaml:"users"`
		Contexts []struct {
			Name    string `yaml:"name"`
			Context struct {
				Namespace string `yaml:"namespace"`
			} `yaml:"context"`
		} `yaml:"contexts"`
	}
	if err := document.Decode(&config); err != nil {
		return KubeconfigMetadata{}, err
	}
	if config.APIVersion != "v1" || config.Kind != "Config" {
		return KubeconfigMetadata{}, errors.New("unsupported kubeconfig schema")
	}
	if len(config.Clusters) > maxKubeEntries || len(config.Users) > maxKubeEntries ||
		len(config.Contexts) > maxKubeEntries {
		return KubeconfigMetadata{}, errors.New("kubeconfig metadata exceeds entry limit")
	}
	if err := validateMetadataValue(config.CurrentContext); err != nil {
		return KubeconfigMetadata{}, err
	}
	contexts := make([]ContextMetadata, 0, len(config.Contexts))
	seen := map[string]struct{}{}
	for _, contextEntry := range config.Contexts {
		if err := ctx.Err(); err != nil {
			return KubeconfigMetadata{}, err
		}
		if contextEntry.Name == "" {
			return KubeconfigMetadata{}, errors.New("kubeconfig context name is empty")
		}
		if err := validateMetadataValue(contextEntry.Name); err != nil {
			return KubeconfigMetadata{}, err
		}
		if err := validateMetadataValue(contextEntry.Context.Namespace); err != nil {
			return KubeconfigMetadata{}, err
		}
		if _, duplicate := seen[contextEntry.Name]; duplicate {
			return KubeconfigMetadata{}, errors.New("duplicate kubeconfig context name")
		}
		seen[contextEntry.Name] = struct{}{}
		contexts = append(contexts, ContextMetadata{
			Name: contextEntry.Name, Namespace: contextEntry.Context.Namespace,
		})
	}
	sort.Slice(contexts, func(left, right int) bool {
		return contexts[left].Name < contexts[right].Name
	})
	return KubeconfigMetadata{
		Path: path, CurrentContext: config.CurrentContext, Contexts: contexts,
		ClusterCount: len(config.Clusters), UserCount: len(config.Users),
	}, nil
}

func rejectAliases(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode || node.Alias != nil {
		return errors.New("YAML aliases are not allowed")
	}
	for _, child := range node.Content {
		if err := rejectAliases(child); err != nil {
			return err
		}
	}
	return nil
}

func validateMetadataValue(value string) error {
	if len(value) > maxMetadataBytes || !utf8.ValidString(value) {
		return errors.New("metadata value is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) || strings.ContainsRune("\u2028\u2029", character) {
			return errors.New("metadata value contains control characters")
		}
	}
	return nil
}
