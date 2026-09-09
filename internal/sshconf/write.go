package sshconf

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// BackupSuffix is appended to a file's name to make its pre-write snapshot.
const BackupSuffix = ".tram.bak"

// SaveOptions controls how a file reaches disk.
type SaveOptions struct {
	// Backup writes <path>.tram.bak before replacing the file.
	Backup bool
	// KeepTimestamped also keeps a dated copy, so that a run of edits does not
	// overwrite the only snapshot with an already modified one.
	KeepTimestamped bool
}

// Save writes the file atomically: a temporary file in the same directory is
// filled, flushed and then renamed over the target, so a crash mid-write leaves
// the original intact rather than a half-written config.
func (f *File) Save(opt SaveOptions) error {
	if f.Path == "" {
		return fmt.Errorf("file has no path")
	}
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	orig, statErr := os.ReadFile(f.Path)
	if statErr == nil && opt.Backup {
		if err := writeFile(f.Path+BackupSuffix, orig, f.perm()); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
		if opt.KeepTimestamped {
			stamp := time.Now().Format("20060102-150405")
			_ = writeFile(fmt.Sprintf("%s.%s%s", f.Path, stamp, BackupSuffix), orig, f.perm())
		}
	}

	tmp, err := os.CreateTemp(dir, ".tram-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(f.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpName, f.perm()); err != nil {
			return fmt.Errorf("set permissions: %w", err)
		}
	}
	if err := os.Rename(tmpName, f.Path); err != nil {
		return fmt.Errorf("replace %s: %w", f.Path, err)
	}
	f.Dirty = false
	return nil
}

func (f *File) perm() os.FileMode {
	if f.mode == 0 {
		return 0o600
	}
	return f.mode
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}

// Original reads the on-disk bytes of the file, for diffing against pending
// in-memory edits. A file that does not exist yet reads as empty.
func (f *File) Original() []byte {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil
	}
	return b
}

// Diff renders a unified diff of the file's pending changes, or "" when there
// are none.
func (f *File) Diff() string {
	return UnifiedDiff(f.Path, f.Original(), f.Bytes())
}
