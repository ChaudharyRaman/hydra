package core

import (
	"os"
	"path/filepath"
	"strings"
)

func savedPathsFile() string { return filepath.Join(HydraDir(), "saved-paths") }

// SavedPaths returns the user's pinned project directories (newline file).
func SavedPaths() []string {
	data, err := os.ReadFile(savedPathsFile())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ToggleSavedPath adds p if absent, removes it if present, and persists.
// Returns true if it was added.
func ToggleSavedPath(p string) bool {
	paths := SavedPaths()
	for i, x := range paths {
		if x == p {
			writeSavedPaths(append(paths[:i], paths[i+1:]...))
			return false
		}
	}
	writeSavedPaths(append(paths, p))
	return true
}

// IsSavedPath reports whether p is pinned.
func IsSavedPath(p string) bool {
	for _, x := range SavedPaths() {
		if x == p {
			return true
		}
	}
	return false
}

func writeSavedPaths(paths []string) {
	os.MkdirAll(HydraDir(), 0o755)
	os.WriteFile(savedPathsFile(), []byte(strings.Join(paths, "\n")+"\n"), 0o644)
}
