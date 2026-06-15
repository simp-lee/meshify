package host

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"lanpanel/internal/assets"
	"lanpanel/internal/render"
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
	Stat(name string) (fs.FileInfo, error)
	Lstat(name string) (fs.FileInfo, error)
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

func (OSFileSystem) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (OSFileSystem) Lstat(name string) (fs.FileInfo, error) {
	return os.Lstat(name)
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
		Args:        []string{"-c", commandAtomicWriteScript, "lanpanel-write-file", name, mode},
		Stdin:       append([]byte(nil), data...),
		DisplayName: "install",
		DisplayArgs: []string{"-m", mode, "--", name},
	})
	return err
}

func (fileSystem CommandFileSystem) Stat(name string) (fs.FileInfo, error) {
	return fileSystem.stat(name, "-L")
}

func (fileSystem CommandFileSystem) Lstat(name string) (fs.FileInfo, error) {
	return fileSystem.stat(name, "")
}

func (fileSystem CommandFileSystem) stat(name string, dereference string) (fs.FileInfo, error) {
	args := []string{"-c", "%f %s %Y %u %g", "--", name}
	if dereference != "" {
		args = []string{dereference, "-c", "%f %s %Y %u %g", "--", name}
	}
	result, err := fileSystem.executor.Run(context.Background(), Command{Name: "stat", Args: args})
	if err != nil {
		if commandRefersToMissingPath(result, err) {
			return nil, &fs.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
		}
		return nil, err
	}

	fields := strings.Fields(result.Stdout)
	if len(fields) != 5 {
		return nil, fmt.Errorf("parse stat output for %s: expected mode size mtime uid gid, got %q", name, strings.TrimSpace(result.Stdout))
	}
	rawMode, parseErr := strconv.ParseUint(fields[0], 16, 32)
	if parseErr != nil {
		return nil, fmt.Errorf("parse raw file mode for %s: %w", name, parseErr)
	}
	size, parseErr := strconv.ParseInt(fields[1], 10, 64)
	if parseErr != nil {
		return nil, fmt.Errorf("parse file size for %s: %w", name, parseErr)
	}
	mtime, parseErr := strconv.ParseInt(fields[2], 10, 64)
	if parseErr != nil {
		return nil, fmt.Errorf("parse file mtime for %s: %w", name, parseErr)
	}
	uid, parseErr := strconv.ParseUint(fields[3], 10, 64)
	if parseErr != nil {
		return nil, fmt.Errorf("parse file uid for %s: %w", name, parseErr)
	}
	gid, parseErr := strconv.ParseUint(fields[4], 10, 64)
	if parseErr != nil {
		return nil, fmt.Errorf("parse file gid for %s: %w", name, parseErr)
	}

	return commandFileInfo{name: filepath.Base(name), size: size, mode: commandFileModeFromRaw(rawMode), modTime: time.Unix(mtime, 0), uid: uid, gid: gid}, nil
}

func commandFileModeFromRaw(rawMode uint64) fs.FileMode {
	mode := fs.FileMode(rawMode & 0o777)
	if rawMode&0o4000 != 0 {
		mode |= fs.ModeSetuid
	}
	if rawMode&0o2000 != 0 {
		mode |= fs.ModeSetgid
	}
	if rawMode&0o1000 != 0 {
		mode |= fs.ModeSticky
	}
	switch rawMode & 0o170000 {
	case 0o100000:
	case 0o040000:
		mode |= fs.ModeDir
	case 0o120000:
		mode |= fs.ModeSymlink
	case 0o010000:
		mode |= fs.ModeNamedPipe
	case 0o140000:
		mode |= fs.ModeSocket
	case 0o020000:
		mode |= fs.ModeCharDevice | fs.ModeDevice
	case 0o060000:
		mode |= fs.ModeDevice
	default:
		mode |= fs.ModeIrregular
	}
	return mode
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

	info, statErr := installer.fs.Lstat(targetPath)
	currentMode := fs.FileMode(0)
	if statErr == nil {
		if err := validateInstallTargetFile(file.HostPath, info); err != nil {
			return result, err
		}
		currentMode = info.Mode().Perm()
		result.Created = false
	} else if os.IsNotExist(statErr) {
		result.Created = true
	} else {
		return result, fmt.Errorf("stat existing file %s: %w", file.HostPath, statErr)
	}

	var currentContent []byte
	if !result.Created {
		readContent, readErr := installer.fs.ReadFile(targetPath)
		if readErr != nil {
			return result, fmt.Errorf("read existing file %s: %w", file.HostPath, readErr)
		}
		currentContent = readContent
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

	if result.ModeChanged && !result.ContentChanged {
		if err := installer.fs.WriteFile(targetPath, currentContent, file.Mode); err != nil {
			return result, fmt.Errorf("write mode-only update for %s: %w", file.HostPath, err)
		}
		markChanged()
	}

	return result, nil
}

func validateInstallTargetFile(hostPath string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to install managed file", hostPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file; refusing to install managed file", hostPath)
	}
	return nil
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
	if info, err := os.Lstat(name); err == nil {
		if err := validateInstallTargetFile(name, info); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
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
if [ -L "$target" ]; then
	echo "$target is a symlink; refusing to install managed file" >&2
	exit 1
fi
if [ -e "$target" ] && [ ! -f "$target" ]; then
	echo "$target exists and is not a regular file; refusing to install managed file" >&2
	exit 1
fi
mv -f -- "$tmp" "$target"
trap - EXIT HUP INT TERM
`

type commandFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	uid     uint64
	gid     uint64
}

func (info commandFileInfo) Name() string       { return info.name }
func (info commandFileInfo) Size() int64        { return info.size }
func (info commandFileInfo) Mode() fs.FileMode  { return info.mode }
func (info commandFileInfo) ModTime() time.Time { return info.modTime }
func (info commandFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info commandFileInfo) Sys() any {
	return struct {
		UID uint64
		GID uint64
	}{UID: info.uid, GID: info.gid}
}

func commandRefersToMissingPath(result Result, err error) bool {
	if err == nil {
		return false
	}

	text := strings.ToLower(strings.TrimSpace(result.Stderr + "\n" + result.Stdout + "\n" + err.Error()))
	return strings.Contains(text, "no such file or directory")
}
