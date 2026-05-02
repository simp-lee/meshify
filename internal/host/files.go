package host

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"meshify/internal/assets"
	"meshify/internal/render"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type FileSystem interface {
	MkdirAll(path string, perm fs.FileMode) error
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Chmod(name string, mode fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
}

type OSFileSystem struct{}

type CommandFileSystem struct {
	executor Executor
}

func (OSFileSystem) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (OSFileSystem) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (OSFileSystem) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return writeFileAtomically(name, data, perm)
}

func (OSFileSystem) Chmod(name string, mode fs.FileMode) error {
	return os.Chmod(name, mode)
}

func (OSFileSystem) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func NewCommandFileSystem(executor Executor) FileSystem {
	return CommandFileSystem{executor: executor}
}

func (fileSystem CommandFileSystem) MkdirAll(path string, perm fs.FileMode) error {
	_, err := fileSystem.executor.Run(context.Background(), Command{
		Name: "mkdir",
		Args: []string{"-p", "-m", fmt.Sprintf("%03o", perm.Perm()), "--", path},
	})
	return err
}

func (fileSystem CommandFileSystem) ReadFile(name string) ([]byte, error) {
	result, err := fileSystem.executor.Run(context.Background(), Command{Name: "cat", Args: []string{"--", name}})
	if err != nil {
		if commandRefersToMissingPath(result, err) {
			return nil, &fs.PathError{Op: "read", Path: name, Err: os.ErrNotExist}
		}
		return nil, err
	}
	return []byte(result.Stdout), nil
}

func (fileSystem CommandFileSystem) WriteFile(name string, data []byte, perm fs.FileMode) error {
	mode := fmt.Sprintf("%03o", perm.Perm())
	_, err := fileSystem.executor.Run(context.Background(), Command{
		Name:        "sh",
		Args:        []string{"-c", commandAtomicWriteScript, "meshify-write-file", name, mode},
		Stdin:       append([]byte(nil), data...),
		DisplayName: "install",
		DisplayArgs: []string{"-m", mode, "--", name},
	})
	return err
}

func (fileSystem CommandFileSystem) Chmod(name string, mode fs.FileMode) error {
	result, err := fileSystem.executor.Run(context.Background(), Command{Name: "chmod", Args: []string{fmt.Sprintf("%03o", mode.Perm()), "--", name}})
	if err != nil {
		if commandRefersToMissingPath(result, err) {
			return &fs.PathError{Op: "chmod", Path: name, Err: os.ErrNotExist}
		}
		return err
	}
	return nil
}

func (fileSystem CommandFileSystem) Stat(name string) (fs.FileInfo, error) {
	result, err := fileSystem.executor.Run(context.Background(), Command{Name: "stat", Args: []string{"-c", "%a", "--", name}})
	if err != nil {
		if commandRefersToMissingPath(result, err) {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
		}
		return nil, err
	}

	modeValue, parseErr := strconv.ParseUint(strings.TrimSpace(result.Stdout), 8, 32)
	if parseErr != nil {
		return nil, fmt.Errorf("parse file mode for %s: %w", name, parseErr)
	}

	return commandFileInfo{name: filepath.Base(name), mode: fs.FileMode(modeValue)}, nil
}

type FileInstaller struct {
	fs      FileSystem
	rootDir string
}

func NewFileInstaller(fileSystem FileSystem, rootDir string) FileInstaller {
	if fileSystem == nil {
		fileSystem = OSFileSystem{}
	}

	return FileInstaller{fs: fileSystem, rootDir: strings.TrimSpace(rootDir)}
}

type FileInstallResult struct {
	SourcePath     string
	HostPath       string
	Changed        bool
	Created        bool
	ContentChanged bool
	ModeChanged    bool
	Activations    []assets.Activation
}

func (installer FileInstaller) Install(files []render.StagedFile) ([]FileInstallResult, error) {
	results := make([]FileInstallResult, 0, len(files))
	for _, file := range files {
		result, err := installer.InstallOne(file)
		if err != nil {
			if result.SourcePath != "" || result.HostPath != "" {
				results = append(results, result)
			}
			return results, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (installer FileInstaller) InstallOne(file render.StagedFile) (FileInstallResult, error) {
	if strings.TrimSpace(file.HostPath) == "" {
		return FileInstallResult{}, fmt.Errorf("staged host path is required")
	}

	targetPath, err := resolveHostPath(installer.rootDir, file.HostPath)
	if err != nil {
		return FileInstallResult{}, err
	}

	result := FileInstallResult{
		SourcePath: file.SourcePath,
		HostPath:   file.HostPath,
	}
	if err := installer.fs.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return result, fmt.Errorf("create parent directory for %s: %w", file.HostPath, err)
	}

	currentContent, readErr := installer.fs.ReadFile(targetPath)
	if readErr == nil {
		result.Created = false
	} else if !os.IsNotExist(readErr) {
		return result, fmt.Errorf("read existing file %s: %w", file.HostPath, readErr)
	} else {
		result.Created = true
	}

	currentMode := fs.FileMode(0)
	if info, statErr := installer.fs.Stat(targetPath); statErr == nil {
		currentMode = info.Mode().Perm()
		result.Created = false
	} else if !os.IsNotExist(statErr) {
		return result, fmt.Errorf("stat existing file %s: %w", file.HostPath, statErr)
	}

	result.ContentChanged = result.Created || !bytes.Equal(currentContent, file.Content)
	result.ModeChanged = result.Created || currentMode != file.Mode.Perm()
	if !result.ContentChanged && !result.ModeChanged {
		return result, nil
	}

	markChanged := func() {
		if result.Changed {
			return
		}
		result.Changed = true
		result.Activations = append([]assets.Activation(nil), file.Activations...)
	}

	if result.ContentChanged {
		if err := installer.fs.WriteFile(targetPath, file.Content, file.Mode); err != nil {
			return result, fmt.Errorf("write %s: %w", file.HostPath, err)
		}
		markChanged()
	}

	if result.ModeChanged {
		if err := installer.fs.Chmod(targetPath, file.Mode); err != nil {
			return result, fmt.Errorf("chmod %s: %w", file.HostPath, err)
		}
		markChanged()
	}

	return result, nil
}

func CollectModifiedPaths(results []FileInstallResult) []string {
	paths := make([]string, 0, len(results))
	seen := map[string]struct{}{}
	for _, result := range results {
		if !result.Changed || result.HostPath == "" {
			continue
		}
		if _, ok := seen[result.HostPath]; ok {
			continue
		}
		seen[result.HostPath] = struct{}{}
		paths = append(paths, result.HostPath)
	}
	return paths
}

func CollectActivations(results []FileInstallResult) []assets.Activation {
	activations := make([]assets.Activation, 0, len(results))
	seen := map[assets.Activation]struct{}{}
	for _, result := range results {
		if !result.Changed {
			continue
		}
		for _, activation := range result.Activations {
			if _, ok := seen[activation]; ok {
				continue
			}
			seen[activation] = struct{}{}
			activations = append(activations, activation)
		}
	}
	return activations
}

func resolveHostPath(rootDir string, hostPath string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(hostPath))
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("host path %q must be absolute", hostPath)
	}
	if strings.TrimSpace(rootDir) == "" {
		return cleaned, nil
	}
	return filepath.Join(rootDir, strings.TrimPrefix(cleaned, string(filepath.Separator))), nil
}

func writeFileAtomically(name string, data []byte, perm fs.FileMode) error {
	targetDir := filepath.Dir(name)
	targetBase := filepath.Base(name)
	temporaryFile, err := os.CreateTemp(targetDir, "."+targetBase+".tmp-*")
	if err != nil {
		return err
	}

	temporaryPath := temporaryFile.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if _, err := temporaryFile.Write(data); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if err := temporaryFile.Chmod(perm.Perm()); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if err := temporaryFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, name); err != nil {
		return err
	}

	removeTemporary = false
	return nil
}

const commandAtomicWriteScript = `
set -eu
target=$1
mode=$2
dir=$(dirname -- "$target")
base=$(basename -- "$target")
tmp=$(mktemp "$dir/.${base}.tmp.XXXXXX")
cleanup() {
	rm -f -- "$tmp"
}
trap cleanup EXIT HUP INT TERM
chmod 600 -- "$tmp"
cat > "$tmp"
chmod "$mode" -- "$tmp"
mv -f -- "$tmp" "$target"
trap - EXIT HUP INT TERM
`

type commandFileInfo struct {
	name string
	mode fs.FileMode
}

func (info commandFileInfo) Name() string       { return info.name }
func (info commandFileInfo) Size() int64        { return 0 }
func (info commandFileInfo) Mode() fs.FileMode  { return info.mode }
func (info commandFileInfo) ModTime() time.Time { return time.Time{} }
func (info commandFileInfo) IsDir() bool        { return false }
func (info commandFileInfo) Sys() any           { return nil }

func commandRefersToMissingPath(result Result, err error) bool {
	if err == nil {
		return false
	}

	text := strings.ToLower(strings.TrimSpace(result.Stderr + "\n" + result.Stdout + "\n" + err.Error()))
	return strings.Contains(text, "no such file or directory")
}
