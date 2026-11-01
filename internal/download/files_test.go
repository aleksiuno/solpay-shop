package download_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aleksiuno/solpay-shop/internal/download"
)

// newFiles opens a directory that holds two product files, a dot file, a subdirectory with a file
// in it, a symbolic link to a product file and one to a file outside the directory.
func newFiles(t *testing.T) *download.Files {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "files")
	write := func(path, content string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	write(filepath.Join(dir, "b-notes.md"), "notes")
	write(filepath.Join(dir, "a-checklist.md"), "checklist")
	write(filepath.Join(dir, ".gitkeep"), "")
	write(filepath.Join(dir, "sub", "inner.md"), "inner")
	write(filepath.Join(base, "secret.txt"), "secret")
	require.NoError(t, os.Symlink("a-checklist.md", filepath.Join(dir, "link-inside.md")))
	require.NoError(t, os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(dir, "link-outside.md")))

	files, err := download.OpenFiles(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, files.Close()) })
	return files
}

// escapes are names that lead outside the directory.
var escapes = []string{"../secret.txt", "sub/../../secret.txt", "link-outside.md"}

func TestFilesList(t *testing.T) {
	names, err := newFiles(t).List()
	require.NoError(t, err)
	assert.Equal(t, []string{"a-checklist.md", "b-notes.md"}, names)
}

func TestFilesExists(t *testing.T) {
	files := newFiles(t)
	tests := map[string]bool{
		"a-checklist.md": true,
		"link-inside.md": true,
		"sub/inner.md":   true,
		"missing.md":     false,
		"sub":            false,
		".":              false,
	}
	for name, want := range tests {
		got, err := files.Exists(name)
		require.NoError(t, err, name)
		assert.Equal(t, want, got, name)
	}
	for _, name := range append(escapes, filepath.Join(t.TempDir(), "x")) {
		got, err := files.Exists(name)
		require.Error(t, err, name)
		assert.False(t, got, name)
	}
}

func TestFilesOpen(t *testing.T) {
	files := newFiles(t)
	for _, name := range []string{"a-checklist.md", "link-inside.md"} { // Exists accepts both
		f, err := files.Open(name)
		require.NoError(t, err, name)
		b, err := io.ReadAll(f)
		require.NoError(t, err)
		require.NoError(t, f.Close())
		assert.Equal(t, "checklist", string(b), name)
	}

	for _, name := range append(escapes, "sub", ".", "missing.md") {
		f, err := files.Open(name)
		require.Error(t, err, name)
		assert.Nil(t, f, name)
	}
}

func TestOpenFilesNeedsADirectory(t *testing.T) {
	_, err := download.OpenFiles(filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "open the files directory")
}
