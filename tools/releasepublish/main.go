package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const releasePublishTimeout = 20 * time.Minute

var (
	repositoryPattern = regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`,
	)
	tagPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-v[0-9]+\.[0-9]+\.[0-9]+$`)
	commitPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	assetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type publishOptions struct {
	Repository string
	Tag        string
	Commit     string
	Assets     []string
}

type releaseAsset struct {
	name string
	path string
	size int64
}

type releaseState struct {
	tag          string
	draft        bool
	targetCommit string
}

type ghRunner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type execGHRunner struct{}

func (execGHRunner) Run(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "gh", arguments...)
	command.Stdin = strings.NewReader("")
	output, err := command.CombinedOutput()
	if err == nil {
		return output, nil
	}
	message := strings.TrimSpace(string(output))
	if len(message) > 4096 {
		message = message[:4096] + "..."
	}
	if message == "" {
		return nil, fmt.Errorf("gh %s: %w", strings.Join(arguments[:2], " "), err)
	}
	return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(arguments[:2], " "), err, message)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), releasePublishTimeout)
	defer cancel()
	if err := run(ctx, os.Args[1:], execGHRunner{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, runner ghRunner) error {
	flags := flag.NewFlagSet("releasepublish", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repository := flags.String("repo", "", "GitHub owner/repository")
	tag := flags.String("tag", "", "immutable release tag")
	commit := flags.String("commit", "", "exact tag commit SHA")
	var assets stringList
	flags.Var(&assets, "asset", "release asset path; repeat for every asset")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("releasepublish accepts only named arguments")
	}
	return publishRelease(ctx, runner, publishOptions{
		Repository: *repository,
		Tag:        *tag,
		Commit:     *commit,
		Assets:     assets,
	})
}

func publishRelease(ctx context.Context, runner ghRunner, options publishOptions) error {
	if err := validatePublishOptions(options); err != nil {
		return err
	}
	workspace, err := os.MkdirTemp("", "ohtools-release-publish-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	if err := os.Chmod(workspace, 0o700); err != nil {
		return err
	}
	assets, err := stageReleaseAssets(options.Assets, filepath.Join(workspace, "assets"))
	if err != nil {
		return err
	}

	remoteCommit, err := runner.Run(
		ctx,
		"api",
		"repos/"+options.Repository+"/commits/"+options.Tag,
		"--jq",
		".sha",
	)
	if err != nil {
		return fmt.Errorf("resolve remote tag commit: %w", err)
	}
	if got := strings.TrimSpace(string(remoteCommit)); got != options.Commit {
		return fmt.Errorf(
			"remote tag commit %q does not match expected commit %q",
			got,
			options.Commit,
		)
	}

	createArguments := []string{
		"release", "create", options.Tag,
		"--repo", options.Repository,
		"--verify-tag",
		"--target", options.Commit,
		"--draft",
		"--generate-notes",
		"--",
	}
	for _, asset := range assets {
		createArguments = append(createArguments, asset.path)
	}
	_, createErr := runner.Run(ctx, createArguments...)

	state, err := inspectRelease(ctx, runner, options.Repository, options.Tag)
	if err != nil {
		if createErr != nil {
			return fmt.Errorf(
				"create draft release: %v; inspect possible partial draft: %w",
				createErr,
				err,
			)
		}
		return fmt.Errorf("inspect created draft release: %w", err)
	}
	if !state.draft {
		if err := verifyPublishedRelease(
			ctx,
			runner,
			options.Repository,
			options,
			state,
			assets,
			workspace,
		); err != nil {
			return fmt.Errorf("verify already-published release: %w", err)
		}
		return nil
	}
	if err := validateDraftState(state, options); err != nil {
		return err
	}
	if err := reconcileDraftAssets(
		ctx,
		runner,
		options.Repository,
		options.Tag,
		assets,
		workspace,
	); err != nil {
		return err
	}

	state, err = inspectRelease(ctx, runner, options.Repository, options.Tag)
	if err != nil {
		return fmt.Errorf("recheck draft release: %w", err)
	}
	if err := validateDraftState(state, options); err != nil {
		return err
	}
	if _, err := runner.Run(
		ctx,
		"release", "edit", options.Tag,
		"--repo", options.Repository,
		"--draft=false",
	); err != nil {
		published, inspectErr := inspectRelease(
			ctx,
			runner,
			options.Repository,
			options.Tag,
		)
		if inspectErr != nil {
			return errors.Join(
				fmt.Errorf("publish verified draft release: %w", err),
				fmt.Errorf("inspect ambiguous publish result: %w", inspectErr),
			)
		}
		if verifyErr := verifyPublishedRelease(
			ctx,
			runner,
			options.Repository,
			options,
			published,
			assets,
			workspace,
		); verifyErr != nil {
			return errors.Join(
				fmt.Errorf("publish verified draft release: %w", err),
				fmt.Errorf("verify ambiguous published release: %w", verifyErr),
			)
		}
		return nil
	}
	published, err := inspectRelease(ctx, runner, options.Repository, options.Tag)
	if err != nil {
		return fmt.Errorf("verify published release: %w", err)
	}
	if err := verifyPublishedRelease(
		ctx,
		runner,
		options.Repository,
		options,
		published,
		assets,
		workspace,
	); err != nil {
		return fmt.Errorf("verify published release: %w", err)
	}
	return nil
}

func verifyPublishedRelease(
	ctx context.Context,
	runner ghRunner,
	repository string,
	options publishOptions,
	state releaseState,
	assets []releaseAsset,
	workspace string,
) error {
	if state.tag != options.Tag ||
		state.targetCommit != options.Commit ||
		state.draft {
		return errors.New("published release state does not match the requested release")
	}
	return verifyCompleteRemoteAssets(
		ctx,
		runner,
		repository,
		options.Tag,
		assets,
		workspace,
	)
}

func validatePublishOptions(options publishOptions) error {
	if !repositoryPattern.MatchString(options.Repository) {
		return errors.New("repository must be an exact owner/name")
	}
	if !tagPattern.MatchString(options.Tag) {
		return errors.New("release tag is invalid")
	}
	if !commitPattern.MatchString(options.Commit) {
		return errors.New("release commit must be a lowercase 40-character SHA-1")
	}
	if len(options.Assets) == 0 || len(options.Assets) > 32 {
		return errors.New("release must contain between 1 and 32 assets")
	}
	return nil
}

func validateDraftState(state releaseState, options publishOptions) error {
	if state.tag != options.Tag {
		return fmt.Errorf("draft release tag %q does not match %q", state.tag, options.Tag)
	}
	if !state.draft {
		return fmt.Errorf("published release already exists for tag %q", options.Tag)
	}
	if state.targetCommit != options.Commit {
		return fmt.Errorf(
			"draft release targets commit %q, expected %q",
			state.targetCommit,
			options.Commit,
		)
	}
	return nil
}

func inspectRelease(
	ctx context.Context,
	runner ghRunner,
	repository,
	tag string,
) (releaseState, error) {
	output, err := runner.Run(
		ctx,
		"release", "view", tag,
		"--repo", repository,
		"--json", "tagName,isDraft,targetCommitish",
		"--jq", `[.tagName, (.isDraft|tostring), .targetCommitish] | @tsv`,
	)
	if err != nil {
		return releaseState{}, err
	}
	fields := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(fields) != 3 || fields[0] == "" || fields[2] == "" {
		return releaseState{}, errors.New("release view returned malformed state")
	}
	var draft bool
	switch fields[1] {
	case "true":
		draft = true
	case "false":
	default:
		return releaseState{}, errors.New("release view returned malformed draft state")
	}
	return releaseState{tag: fields[0], draft: draft, targetCommit: fields[2]}, nil
}

func reconcileDraftAssets(
	ctx context.Context,
	runner ghRunner,
	repository,
	tag string,
	assets []releaseAsset,
	workspace string,
) error {
	expected := make(map[string]releaseAsset, len(assets))
	for _, asset := range assets {
		expected[asset.name] = asset
	}
	remote, err := listRemoteAssets(ctx, runner, repository, tag)
	if err != nil {
		return err
	}
	present := make(map[string]struct{}, len(remote))
	for _, name := range remote {
		asset, exists := expected[name]
		if !exists {
			return fmt.Errorf("draft release contains unexpected asset %q", name)
		}
		if _, duplicate := present[name]; duplicate {
			return fmt.Errorf("draft release reports duplicate asset %q", name)
		}
		present[name] = struct{}{}
		if err := verifyRemoteAsset(ctx, runner, repository, tag, asset, workspace); err != nil {
			return err
		}
	}
	missing := make([]string, 0, len(assets))
	for _, asset := range assets {
		if _, exists := present[asset.name]; !exists {
			missing = append(missing, asset.path)
		}
	}
	if len(missing) > 0 {
		uploadArguments := []string{
			"release", "upload", tag,
			"--repo", repository,
			"--",
		}
		uploadArguments = append(uploadArguments, missing...)
		if _, err := runner.Run(ctx, uploadArguments...); err != nil {
			return fmt.Errorf("upload missing draft assets: %w", err)
		}
	}
	return verifyCompleteRemoteAssets(ctx, runner, repository, tag, assets, workspace)
}

func verifyCompleteRemoteAssets(
	ctx context.Context,
	runner ghRunner,
	repository,
	tag string,
	assets []releaseAsset,
	workspace string,
) error {
	remote, err := listRemoteAssets(ctx, runner, repository, tag)
	if err != nil {
		return err
	}
	expected := make(map[string]releaseAsset, len(assets))
	for _, asset := range assets {
		expected[asset.name] = asset
	}
	if len(remote) != len(expected) {
		return fmt.Errorf(
			"draft release asset count %d does not match expected count %d",
			len(remote),
			len(expected),
		)
	}
	seen := make(map[string]struct{}, len(remote))
	for _, name := range remote {
		asset, exists := expected[name]
		if !exists {
			return fmt.Errorf("draft release contains unexpected asset %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("draft release reports duplicate asset %q", name)
		}
		seen[name] = struct{}{}
		if err := verifyRemoteAsset(ctx, runner, repository, tag, asset, workspace); err != nil {
			return err
		}
	}
	return nil
}

func listRemoteAssets(
	ctx context.Context,
	runner ghRunner,
	repository,
	tag string,
) ([]string, error) {
	output, err := runner.Run(
		ctx,
		"release", "view", tag,
		"--repo", repository,
		"--json", "assets",
		"--jq", ".assets[].name",
	)
	if err != nil {
		return nil, fmt.Errorf("list draft release assets: %w", err)
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return []string{}, nil
	}
	names := strings.Split(trimmed, "\n")
	for _, name := range names {
		if !assetNamePattern.MatchString(name) {
			return nil, fmt.Errorf("draft release contains unsafe asset name %q", name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func verifyRemoteAsset(
	ctx context.Context,
	runner ghRunner,
	repository,
	tag string,
	asset releaseAsset,
	workspace string,
) error {
	directory, err := os.MkdirTemp(workspace, "download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	if _, err := runner.Run(
		ctx,
		"release", "download", tag,
		"--repo", repository,
		"--pattern", asset.name,
		"--dir", directory,
	); err != nil {
		return fmt.Errorf("download draft asset %q: %w", asset.name, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != asset.name {
		return fmt.Errorf("download for asset %q returned unexpected files", asset.name)
	}
	matches, err := equalRegularFiles(
		asset.path,
		filepath.Join(directory, asset.name),
		asset.size,
	)
	if err != nil {
		return fmt.Errorf("inspect downloaded asset %q: %w", asset.name, err)
	}
	if !matches {
		return fmt.Errorf("draft asset %q does not match local asset", asset.name)
	}
	return nil
}

func stageReleaseAssets(paths []string, destination string) ([]releaseAsset, error) {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return nil, err
	}
	assets := make([]releaseAsset, 0, len(paths))
	seen := map[string]struct{}{}
	for _, source := range paths {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(absolute)
		if !assetNamePattern.MatchString(name) {
			return nil, fmt.Errorf("unsafe release asset name %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("duplicate release asset name %q", name)
		}
		seen[name] = struct{}{}
		staged := filepath.Join(destination, name)
		size, err := copyRegularFile(absolute, staged)
		if err != nil {
			return nil, fmt.Errorf("stage release asset %q: %w", name, err)
		}
		if size == 0 {
			return nil, fmt.Errorf("release asset %q is empty", name)
		}
		assets = append(assets, releaseAsset{
			name: name, path: staged, size: size,
		})
	}
	return assets, nil
}

func copyRegularFile(source, destination string) (int64, error) {
	sourceFile, sourceInfo, err := openRegularNonSymlink(source)
	if err != nil {
		return 0, err
	}
	defer sourceFile.Close()
	destinationFile, err := os.OpenFile(
		destination,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(destinationFile, sourceFile)
	if copyErr != nil {
		_ = destinationFile.Close()
		return 0, copyErr
	}
	if written != sourceInfo.Size() {
		_ = destinationFile.Close()
		return 0, errors.New("release asset changed while it was staged")
	}
	if err := destinationFile.Sync(); err != nil {
		_ = destinationFile.Close()
		return 0, err
	}
	if err := destinationFile.Close(); err != nil {
		return 0, err
	}
	return written, nil
}

func equalRegularFiles(leftPath, rightPath string, expectedSize int64) (bool, error) {
	left, leftInfo, err := openRegularNonSymlink(leftPath)
	if err != nil {
		return false, err
	}
	defer left.Close()
	right, rightInfo, err := openRegularNonSymlink(rightPath)
	if err != nil {
		return false, err
	}
	defer right.Close()
	if leftInfo.Size() != expectedSize || rightInfo.Size() != expectedSize {
		return false, nil
	}
	leftBuffer := make([]byte, 64<<10)
	rightBuffer := make([]byte, len(leftBuffer))
	remaining := expectedSize
	for remaining > 0 {
		chunk := int64(len(leftBuffer))
		if remaining < chunk {
			chunk = remaining
		}
		if _, err := io.ReadFull(left, leftBuffer[:chunk]); err != nil {
			return false, err
		}
		if _, err := io.ReadFull(right, rightBuffer[:chunk]); err != nil {
			return false, err
		}
		if !bytes.Equal(leftBuffer[:chunk], rightBuffer[:chunk]) {
			return false, nil
		}
		remaining -= chunk
	}
	var extra [1]byte
	leftCount, leftErr := left.Read(extra[:])
	rightCount, rightErr := right.Read(extra[:])
	if leftCount != 0 || rightCount != 0 {
		return false, nil
	}
	if !errors.Is(leftErr, io.EOF) || !errors.Is(rightErr, io.EOF) {
		return false, errors.New("asset comparison did not reach EOF")
	}
	return true, nil
}

func openRegularNonSymlink(path string) (*os.File, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, errors.New("path must be a regular non-symlink file")
	}
	file, err := os.Open(path) // #nosec G304 -- explicit CI artifact with descriptor identity checks.
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(after, opened) {
		_ = file.Close()
		return nil, nil, errors.New("path changed while it was being opened")
	}
	return file, opened, nil
}

type stringList []string

func (values *stringList) String() string {
	return strings.Join(*values, ",")
}

func (values *stringList) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("asset path must not be empty")
	}
	*values = append(*values, value)
	return nil
}
