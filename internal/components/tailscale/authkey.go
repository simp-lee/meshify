package tailscale

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func ReadAuthKeyFile(path string) (string, error) {
	if err := validateAuthKeyFileParents(path); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open tailscale auth key file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat tailscale auth key file: %w", err)
	}
	if err := validateAuthKeyFileInfo(info); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", fmt.Errorf("read tailscale auth key file: %w", err)
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("tailscale auth key file is too large")
	}
	key := strings.TrimSpace(string(data))
	if err := validateAuthKey(key, string(data)); err != nil {
		return "", err
	}
	return key, nil
}

func validateAuthKeyFileInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("tailscale auth key file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("tailscale auth key file must be root-only, for example mode 0600")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
		return fmt.Errorf("tailscale auth key file must be owned by root")
	}
	return nil
}

func validateAuthKeyFileParents(path string) error {
	dir := filepath.Dir(path)
	immediateParent := dir
	for {
		info, err := os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("tailscale auth key file parent directory %s is not available: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("tailscale auth key file parent directory %s must not be a symlink", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("tailscale auth key file parent path %s must be a directory", dir)
		}
		if info.Mode().Perm()&0o022 != 0 && (dir == immediateParent || info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("tailscale auth key file parent directory %s must not be writable by group or others", dir)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
			return fmt.Errorf("tailscale auth key file parent directory %s must be owned by root", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func validateAuthKey(key string, raw string) error {
	if key == "" {
		return fmt.Errorf("tailscale auth key file is empty")
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("tailscale auth key file must contain exactly one token")
	}
	if strings.Count(strings.TrimRight(raw, "\r\n"), "\n") > 0 {
		return fmt.Errorf("tailscale auth key file must contain exactly one token")
	}
	if !strings.HasPrefix(key, "tskey-auth-") && !strings.HasPrefix(key, "hskey-auth-") {
		return fmt.Errorf("tailscale auth key file must contain a pre-authentication key with prefix tskey-auth- or hskey-auth-")
	}
	return nil
}
