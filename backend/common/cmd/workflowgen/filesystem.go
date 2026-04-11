package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileSystem abstracts file system operations to enable in-memory testing.
// This interface separates the generator logic from actual disk I/O, allowing
// tests to run without creating temporary files.
type FileSystem interface {
	// ReadFile reads the entire file content
	ReadFile(path string) ([]byte, error)

	// WriteFile writes content to a file
	WriteFile(path string, data []byte, perm os.FileMode) error

	// ReadDir reads directory entries
	ReadDir(path string) ([]fs.DirEntry, error)

	// Walk recursively walks a directory tree
	Walk(root string, fn filepath.WalkFunc) error

	// Rel returns the relative path from basepath to targpath
	Rel(basepath, targpath string) (string, error)

	// Join joins path elements
	Join(elem ...string) string

	// Dir returns the directory component of path
	Dir(path string) string
}

// osFileSystem is the production implementation that uses real disk I/O
type osFileSystem struct{}

func (osFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (osFileSystem) ReadDir(path string) ([]fs.DirEntry, error) {
	return os.ReadDir(path)
}

func (osFileSystem) Walk(root string, fn filepath.WalkFunc) error {
	return filepath.Walk(root, fn)
}

func (osFileSystem) Rel(basepath, targpath string) (string, error) {
	return filepath.Rel(basepath, targpath)
}

func (osFileSystem) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (osFileSystem) Dir(path string) string {
	return filepath.Dir(path)
}

// memFileSystem is an in-memory implementation for testing
type memFileSystem struct {
	files map[string][]byte
}

func newMemFileSystem() *memFileSystem {
	return &memFileSystem{
		files: make(map[string][]byte),
	}
}

func (m *memFileSystem) ReadFile(path string) ([]byte, error) {
	content, ok := m.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return content, nil
}

func (m *memFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	m.files[path] = data
	return nil
}

func (m *memFileSystem) ReadDir(path string) ([]fs.DirEntry, error) {
	// Normalize path
	if path == "" {
		path = "."
	}

	// Find all files in this directory (not subdirectories)
	var entries []fs.DirEntry
	seen := make(map[string]bool)

	for filePath := range m.files {
		// Check if file is in this directory
		dir := m.Dir(filePath)
		if dir != path {
			continue
		}

		// Extract filename
		parts := strings.Split(filePath, "/")
		if len(parts) == 0 {
			continue
		}
		name := parts[len(parts)-1]

		if !seen[name] {
			seen[name] = true
			entries = append(entries, &memDirEntry{
				name:  name,
				isDir: false,
			})
		}
	}

	// Add subdirectories
	for filePath := range m.files {
		// Get relative path from the directory
		if !strings.HasPrefix(filePath, path+"/") && filePath != path {
			continue
		}

		// Remove prefix to get relative part
		rel := strings.TrimPrefix(filePath, path+"/")
		parts := strings.Split(rel, "/")
		if len(parts) > 1 {
			// This file is in a subdirectory
			subdir := parts[0]
			if !seen[subdir] {
				seen[subdir] = true
				entries = append(entries, &memDirEntry{
					name:  subdir,
					isDir: true,
				})
			}
		}
	}

	// Sort for consistent ordering
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	return entries, nil
}

func (m *memFileSystem) Walk(root string, fn filepath.WalkFunc) error {
	// Collect all paths under root
	var paths []string
	for path := range m.files {
		if strings.HasPrefix(path, root) || path == root {
			paths = append(paths, path)
		}
	}

	// Sort paths for deterministic traversal
	sort.Strings(paths)

	// Walk each path
	for _, path := range paths {
		info := &memFileInfo{
			name: filepath.Base(path),
			size: int64(len(m.files[path])),
		}

		err := fn(path, info, nil)
		if err != nil {
			if errors.Is(err, filepath.SkipDir) {
				continue
			}
			return err
		}
	}

	return nil
}

func (m *memFileSystem) Rel(basepath, targpath string) (string, error) {
	// Simple implementation for testing - just use filepath.Rel
	return filepath.Rel(basepath, targpath)
}

func (m *memFileSystem) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (m *memFileSystem) Dir(path string) string {
	return filepath.Dir(path)
}

// memDirEntry implements fs.DirEntry for in-memory filesystem
type memDirEntry struct {
	name  string
	isDir bool
}

func (e *memDirEntry) Name() string               { return e.name }
func (e *memDirEntry) IsDir() bool                { return e.isDir }
func (e *memDirEntry) Type() fs.FileMode          { return 0 }
func (e *memDirEntry) Info() (fs.FileInfo, error) { return &memFileInfo{name: e.name}, nil }

// memFileInfo implements fs.FileInfo for in-memory filesystem
type memFileInfo struct {
	name string
	size int64
}

func (i *memFileInfo) Name() string       { return i.name }
func (i *memFileInfo) Size() int64        { return i.size }
func (i *memFileInfo) Mode() fs.FileMode  { return 0o644 }
func (i *memFileInfo) ModTime() time.Time { return time.Time{} }
func (i *memFileInfo) IsDir() bool        { return false }
func (i *memFileInfo) Sys() interface{}   { return nil }
