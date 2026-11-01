package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Only explicitly selected files enter the immutable revision. In particular,
// neighboring inputs, keys, state and repository files are not swept up.
func readRevisionFiles(filename string, includes []string, excluded ...string) (map[string]string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	excludedInfo := []os.FileInfo{}
	for _, name := range excluded {
		if name == "" {
			continue
		}
		info, err := os.Stat(name)
		if err != nil {
			return nil, err
		}
		excludedInfo = append(excludedInfo, info)
	}
	files := map[string]string{}
	total := 0
	read := func(name, destination string) error {
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("revision file must be regular and not a symlink: %s", name)
		}
		for _, excluded := range excludedInfo {
			if os.SameFile(info, excluded) {
				return fmt.Errorf("inputs file cannot be included in revision: %s", name)
			}
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return err
		}
		for _, excluded := range excludedInfo {
			if os.SameFile(opened, excluded) {
				return fmt.Errorf("inputs file cannot be included in revision: %s", name)
			}
		}
		raw, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		if err != nil {
			return err
		}
		if !utf8.Valid(raw) {
			return fmt.Errorf("revision file is not UTF-8 text: %s", name)
		}
		total += len(raw)
		if total > 8<<20 {
			return fmt.Errorf("revision exceeds 8 MiB")
		}
		if _, exists := files[destination]; exists {
			return fmt.Errorf("duplicate revision path: %s", destination)
		}
		files[destination] = string(raw)
		return nil
	}
	if err = read(filepath.Base(absolute), "field.sysbox.hcl"); err != nil {
		return nil, err
	}
	for _, include := range includes {
		if !fs.ValidPath(include) || include == "." || strings.Contains(include, "\\") {
			return nil, fmt.Errorf("--include must be a relative project path without ..")
		}
		for _, segment := range strings.Split(include, "/") {
			if strings.HasPrefix(segment, ".") {
				return nil, fmt.Errorf("hidden paths cannot be included")
			}
		}
		// Check each ancestor too: WalkDir does not inspect symlinked parents.
		for prefix := include; prefix != "."; prefix = path.Dir(prefix) {
			info, err := root.Lstat(prefix)
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("revision symlink is not allowed: %s", prefix)
			}
		}
		err = fs.WalkDir(root.FS(), include, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(entry.Name(), ".") {
				return fmt.Errorf("hidden revision path is not allowed: %s", name)
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("revision symlink is not allowed: %s", name)
			}
			return read(name, name)
		})
		if err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		return nil, err
	}
	if len(encoded) > 8<<20 {
		return nil, fmt.Errorf("encoded revision exceeds 8 MiB")
	}
	return files, nil
}

func readLifecycleInputs(filename string) (map[string]string, error) {
	if filename == "" {
		return nil, nil
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("inputs exceed 1 MiB")
	}
	var inputs map[string]string
	if json.Unmarshal(raw, &inputs) != nil || inputs == nil {
		return nil, fmt.Errorf("inputs must be a JSON object of string values")
	}
	return inputs, nil
}
