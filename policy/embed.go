// Copyright 2026 VSJin. Licensed under Apache-2.0.
package policy

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed skills
var builtin embed.FS

// Materialize makes the versioned skills available independently of the cwd.
func Materialize() (string, func(), error) {
	dir, err := os.MkdirTemp("", "vsjin-skills-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	err = fs.WalkDir(builtin, "skills", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, readErr := builtin.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0600)
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(dir, "skills"), cleanup, nil
}
