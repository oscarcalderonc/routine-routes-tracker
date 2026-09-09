// Package drive copies newly recorded files from cloud storage into a local
// directory.
//
// The copy is delegated to rclone rather than implemented against a provider's
// API, which keeps provider credentials and their refresh out of this program
// entirely: rclone owns the remote's configuration and this package only asks
// it to mirror a folder.
package drive

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Puller copies files from a configured remote into a local directory.
type Puller struct {
	// Bin is the rclone executable.
	Bin string
	// Config is the path to the rclone configuration file.
	Config string
	// Remote is the configured remote name, such as "gdrive".
	Remote string
	// Folder is the path within the remote to mirror.
	Folder string
	// Dest is the local directory that files are copied into.
	Dest string
	// Timeout bounds a single pull.
	Timeout time.Duration
}

// Configured reports whether a remote has been set up. When it has not, the
// tracker still works: files placed in the inbox directory by any other means
// are picked up just the same.
func (p Puller) Configured() bool {
	return p.Remote != "" && p.Bin != ""
}

// WriteConfig materialises a base64-encoded rclone configuration supplied as a
// secret. It does nothing when the encoded value is empty.
func WriteConfig(path, encoded string) error {
	if encoded == "" {
		return nil
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return fmt.Errorf("decode rclone config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	// The file holds credentials, so it is readable only by the owner.
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write rclone config: %w", err)
	}
	return nil
}

// Pull mirrors the remote folder into the destination directory. Existing files
// are left alone, so repeated pulls transfer only what is new.
func (p Puller) Pull(ctx context.Context) error {
	if !p.Configured() {
		return nil
	}
	if err := os.MkdirAll(p.Dest, 0o755); err != nil {
		return fmt.Errorf("create inbox: %w", err)
	}

	timeout := p.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	src := p.Remote + ":" + p.Folder
	args := []string{"copy", src, p.Dest, "--include", "*.gpx", "--no-traverse"}
	if p.Config != "" {
		args = append(args, "--config", p.Config)
	}

	cmd := exec.CommandContext(ctx, p.Bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rclone copy from %s: %w: %s", src, err, strings.TrimSpace(string(out)))
	}
	return nil
}
