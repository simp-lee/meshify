package edgeone

import (
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeFS struct {
	files map[string]fakeFile
}

type fakeFile struct {
	content      string
	mode         fs.FileMode
	uid          uint32
	size         int64
	sys          any
	unknownOwner bool
}

type fakeInfo struct {
	name string
	file fakeFile
}

func (f fakeFS) Lstat(path string) (fs.FileInfo, error) {
	file, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	if file.size == 0 && file.content != "" {
		file.size = int64(len(file.content))
	}
	return fakeInfo{name: path, file: file}, nil
}

func (f fakeFS) ReadFile(path string) ([]byte, error) {
	file, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(file.content), nil
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return i.file.size }
func (i fakeInfo) Mode() fs.FileMode  { return i.file.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.file.mode.IsDir() }
func (i fakeInfo) Sys() any {
	if i.file.unknownOwner {
		return nil
	}
	if i.file.sys != nil {
		return i.file.sys
	}
	return &syscall.Stat_t{Uid: i.file.uid}
}

func validCredentialFS(envContent string) fakeFS {
	files := map[string]fakeFile{
		"/":                                  {mode: fs.ModeDir | 0o755},
		"/etc":                               {mode: fs.ModeDir | 0o755},
		"/etc/lanpanel":                      {mode: fs.ModeDir | 0o755},
		"/etc/lanpanel/realip":               {mode: fs.ModeDir | 0o755},
		"/etc/lanpanel/realip/prod.env":      {content: envContent, mode: 0o600},
		"/etc/lanpanel/realip/secret-id":     {content: "secret-id\n", mode: 0o600},
		"/etc/lanpanel/realip/secret-key":    {content: "secret-key\n", mode: 0o600},
		"/etc/lanpanel/realip/session-token": {content: "session-token\n", mode: 0o600},
	}
	for path, file := range files {
		if file.mode.IsDir() {
			file.size = 1
		} else if file.size == 0 {
			file.size = int64(len(file.content))
		}
		files[path] = file
	}
	return fakeFS{files: files}
}

func TestLoadCredentialsFromEnvFileReadsOnlyFileReferences(t *testing.T) {
	t.Parallel()

	env := strings.Join([]string{
		"TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id",
		"TENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key",
		"TENCENTCLOUD_SESSION_TOKEN_FILE=/etc/lanpanel/realip/session-token",
	}, "\n")
	credentials, err := LoadCredentialsFromEnvFile(validCredentialFS(env), "/etc/lanpanel/realip/prod.env")
	if err != nil {
		t.Fatalf("LoadCredentialsFromEnvFile() error = %v", err)
	}
	if credentials.SecretID != "secret-id" || credentials.SecretKey != "secret-key" || credentials.SessionToken != "session-token" {
		t.Fatalf("credentials = %#v, want values from referenced files", credentials)
	}
}

func TestLoadCredentialsFromEnvFileAcceptsCommandFileOwnerFields(t *testing.T) {
	t.Parallel()

	env := "TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key"
	fileSystem := validCredentialFS(env)
	for path, file := range fileSystem.files {
		file.sys = struct {
			UID uint64
			GID uint64
		}{UID: uint64(file.uid), GID: 0}
		fileSystem.files[path] = file
	}
	if _, err := LoadCredentialsFromEnvFile(fileSystem, "/etc/lanpanel/realip/prod.env"); err != nil {
		t.Fatalf("LoadCredentialsFromEnvFile() error = %v, want command owner fields accepted", err)
	}
}

func TestLoadCredentialsFromEnvFileRejectsRelativeEnvFilePath(t *testing.T) {
	t.Parallel()

	env := "TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key"
	for _, path := range []string{"prod.env", "/etc/lanpanel/realip/../realip/prod.env"} {
		_, err := LoadCredentialsFromEnvFile(validCredentialFS(env), path)
		if err == nil || !strings.Contains(err.Error(), "edgeone env_file must be a clean absolute path") {
			t.Fatalf("LoadCredentialsFromEnvFile(%q) error = %v, want clean absolute path failure", path, err)
		}
	}
}

func TestLoadCredentialsFromEnvFileRejectsRawAndUnusedVariables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "raw secret id", env: "TENCENTCLOUD_SECRET_ID=raw\nTENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key", want: "TENCENTCLOUD_SECRET_ID is not supported"},
		{name: "raw secret key", env: "TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_KEY=raw", want: "TENCENTCLOUD_SECRET_KEY is not supported"},
		{name: "lego option", env: "TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key\nLEGO_DNS_TIMEOUT=30", want: "LEGO_DNS_TIMEOUT is not supported"},
		{name: "missing id", env: "TENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key", want: "TENCENTCLOUD_SECRET_ID_FILE is required"},
		{name: "duplicate key", env: "TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key", want: "TENCENTCLOUD_SECRET_ID_FILE is duplicated"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadCredentialsFromEnvFile(validCredentialFS(tt.env), "/etc/lanpanel/realip/prod.env")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadCredentialsFromEnvFile() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadCredentialsFromEnvFileRejectsMalformedSecretFileTokens(t *testing.T) {
	t.Parallel()

	env := strings.Join([]string{
		"TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id",
		"TENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key",
		"TENCENTCLOUD_SESSION_TOKEN_FILE=/etc/lanpanel/realip/session-token",
	}, "\n")
	tests := []struct {
		name    string
		path    string
		content string
		want    string
	}{
		{name: "secret id multiline", path: "/etc/lanpanel/realip/secret-id", content: "secret\nid\n", want: "TENCENTCLOUD_SECRET_ID_FILE referenced file /etc/lanpanel/realip/secret-id must contain exactly one token"},
		{name: "secret key space", path: "/etc/lanpanel/realip/secret-key", content: "secret key\n", want: "TENCENTCLOUD_SECRET_KEY_FILE referenced file /etc/lanpanel/realip/secret-key must contain exactly one token"},
		{name: "session token tab", path: "/etc/lanpanel/realip/session-token", content: "session\ttoken\n", want: "TENCENTCLOUD_SESSION_TOKEN_FILE referenced file /etc/lanpanel/realip/session-token must contain exactly one token"},
		{name: "secret key nul", path: "/etc/lanpanel/realip/secret-key", content: "secret\x00key\n", want: "TENCENTCLOUD_SECRET_KEY_FILE referenced file /etc/lanpanel/realip/secret-key must contain exactly one token"},
		{name: "secret id leading space", path: "/etc/lanpanel/realip/secret-id", content: " secret-id\n", want: "TENCENTCLOUD_SECRET_ID_FILE referenced file /etc/lanpanel/realip/secret-id must contain exactly one token"},
		{name: "secret key trailing space", path: "/etc/lanpanel/realip/secret-key", content: "secret-key \n", want: "TENCENTCLOUD_SECRET_KEY_FILE referenced file /etc/lanpanel/realip/secret-key must contain exactly one token"},
		{name: "session token leading tab", path: "/etc/lanpanel/realip/session-token", content: "\tsession-token\n", want: "TENCENTCLOUD_SESSION_TOKEN_FILE referenced file /etc/lanpanel/realip/session-token must contain exactly one token"},
		{name: "secret key extra blank line", path: "/etc/lanpanel/realip/secret-key", content: "secret-key\n\n", want: "TENCENTCLOUD_SECRET_KEY_FILE referenced file /etc/lanpanel/realip/secret-key must contain exactly one token"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fileSystem := validCredentialFS(env)
			file := fileSystem.files[tt.path]
			file.content = tt.content
			file.size = int64(len(tt.content))
			fileSystem.files[tt.path] = file
			_, err := LoadCredentialsFromEnvFile(fileSystem, "/etc/lanpanel/realip/prod.env")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadCredentialsFromEnvFile() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadCredentialsFromEnvFileRejectsUnsafePermissions(t *testing.T) {
	t.Parallel()

	env := "TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key"
	fileSystem := validCredentialFS(env)
	unsafeEnv := fileSystem.files["/etc/lanpanel/realip/prod.env"]
	unsafeEnv.mode = 0o640
	fileSystem.files["/etc/lanpanel/realip/prod.env"] = unsafeEnv
	if _, err := LoadCredentialsFromEnvFile(fileSystem, "/etc/lanpanel/realip/prod.env"); err == nil || !strings.Contains(err.Error(), "must be root-only") {
		t.Fatalf("LoadCredentialsFromEnvFile() error = %v, want root-only failure", err)
	}

	fileSystem = validCredentialFS(env)
	nonRootSecret := fileSystem.files["/etc/lanpanel/realip/secret-key"]
	nonRootSecret.uid = 1000
	fileSystem.files["/etc/lanpanel/realip/secret-key"] = nonRootSecret
	if _, err := LoadCredentialsFromEnvFile(fileSystem, "/etc/lanpanel/realip/prod.env"); err == nil || !strings.Contains(err.Error(), "must be owned by root") {
		t.Fatalf("LoadCredentialsFromEnvFile() error = %v, want root owner failure", err)
	}
}

func TestLoadCredentialsFromEnvFileRejectsUninspectableOwnership(t *testing.T) {
	t.Parallel()

	env := "TENCENTCLOUD_SECRET_ID_FILE=/etc/lanpanel/realip/secret-id\nTENCENTCLOUD_SECRET_KEY_FILE=/etc/lanpanel/realip/secret-key"
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "credential file", path: "/etc/lanpanel/realip/secret-key", want: "TENCENTCLOUD_SECRET_KEY_FILE /etc/lanpanel/realip/secret-key owner could not be inspected"},
		{name: "parent directory", path: "/etc/lanpanel/realip", want: "edgeone env_file parent directory /etc/lanpanel/realip owner could not be inspected"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fileSystem := validCredentialFS(env)
			file := fileSystem.files[tt.path]
			file.unknownOwner = true
			fileSystem.files[tt.path] = file
			_, err := LoadCredentialsFromEnvFile(fileSystem, "/etc/lanpanel/realip/prod.env")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadCredentialsFromEnvFile() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
