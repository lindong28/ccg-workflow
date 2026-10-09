package main

import (
	"errors"
	"fmt"
	"os"
)

// validateWorkDir catches invalid paths, not a valid but unintended directory.
func validateWorkDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return fmt.Errorf("invalid workdir %q: %w; pass an existing directory as the workdir argument", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid workdir %q: not a directory", dir)
	}
	return nil
}
