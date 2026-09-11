package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
)

// Workspace gives tools read-only, confined access to the checked-out
// repository. Access goes through os.Root, which refuses to resolve any path
// outside the root, and symbolic links are refused outright: a pull request
// could otherwise add "notes.txt -> .git/config" and read a blocked file
// through an innocent-looking name.
type Workspace struct {
	root *os.Root
}

// OpenWorkspace opens dir as the repository root.
func OpenWorkspace(dir string) (*Workspace, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening repository workspace: %w", err)
	}
	return &Workspace{root: root}, nil
}

// Close releases the root directory handle.
func (w *Workspace) Close() error { return w.root.Close() }

var (
	// blockedNames are files that commonly hold credentials. Reading them is
	// refused even if they exist in the repository: whatever the model reads
	// can end up in the model provider's logs or in a public review comment.
	blockedNames = []string{
		".env", ".git-credentials", ".netrc", ".npmrc", ".pypirc", ".dockercfg",
		"credentials.json", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	}
	blockedExtensions = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore"}
)

// cleanPath validates a path supplied by the model and returns it in clean,
// slash-separated form. When allowRoot is set, "" and "." mean the root.
func cleanPath(p string, allowRoot bool) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		if allowRoot {
			return ".", nil
		}
		return "", errors.New("path is required")
	}
	switch {
	case strings.ContainsAny(p, "\x00\\"):
		return "", errors.New("path must be a slash-separated repository path")
	case strings.HasPrefix(p, "/"):
		return "", errors.New("path must be relative to the repository root")
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path must stay inside the repository")
	}
	if blocked(clean) {
		return "", fmt.Errorf("access to %q is not allowed", clean)
	}
	return clean, nil
}

func blocked(p string) bool {
	if slices.Contains(strings.Split(p, "/"), ".git") {
		return true
	}
	base := strings.ToLower(path.Base(p))
	if slices.Contains(blockedNames, base) || slices.Contains(blockedExtensions, path.Ext(base)) {
		return true
	}
	return strings.HasPrefix(base, ".env.") && base != ".env.example"
}

// rejectSymlinks refuses p if it or any of its parent directories is a
// symbolic link.
func (w *Workspace) rejectSymlinks(p string) error {
	if p == "." {
		return nil
	}
	parts := strings.Split(p, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		info, err := w.root.Lstat(prefix)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%q does not exist at the reviewed commit", p)
			}
			return fmt.Errorf("inspecting %q: %w", prefix, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%q is a symbolic link; links are not followed", prefix)
		}
	}
	return nil
}
