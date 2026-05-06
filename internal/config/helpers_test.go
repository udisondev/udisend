package config_test

import (
	"io/fs"
	"os"
	"testing"
)

func statMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
