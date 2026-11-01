// Package download reads the product files that paid orders download.
package download

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Files reads product files from one directory. Every access goes through an os.Root, so a name
// cannot reach outside the directory, whether through "..", an absolute path or a symbolic link
// that points elsewhere.
type Files struct {
	root *os.Root
}

// OpenFiles opens dir for reading product files.
func OpenFiles(dir string) (*Files, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the files directory: %w", err)
	}
	return &Files{root: root}, nil
}

// Close releases the directory.
func (f *Files) Close() error {
	return f.root.Close()
}

// List returns the names of the regular files at the top of the directory, sorted. It leaves out
// subdirectories, symbolic links and dot files, such as .gitkeep, which are never products.
func (f *Files) List() ([]string, error) {
	entries, err := fs.ReadDir(f.root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("list the files directory: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Exists reports whether name is a regular file in the directory. A name that does not exist, or
// that is a directory, gives false; a name that leads outside the directory is an error.
func (f *Files) Exists(name string) (bool, error) {
	info, err := f.root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// Open opens name for reading. It refuses a name that leads outside the directory, and anything
// that is not a regular file.
func (f *Files) Open(name string) (*os.File, error) {
	file, err := f.root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("open %s: not a regular file", name), file.Close())
	}
	return file, nil
}
