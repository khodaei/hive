package logrotate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// 1KB max size for easy testing
	w, err := New(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Override maxBytes for testing (1KB)
	w.maxBytes = 1024

	// Write enough data to trigger rotation
	line := strings.Repeat("x", 100) + "\n" // 101 bytes
	for i := 0; i < 15; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	w.Close()

	// Current file should exist and be small
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal("current log file should exist")
	}
	if info.Size() > 1024 {
		t.Errorf("current file too large: %d bytes", info.Size())
	}

	// At least .1 backup should exist
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Error("backup .1 should exist")
	}
}

func TestMaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := New(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	w.maxBytes = 100 // tiny threshold

	// Write enough to trigger many rotations
	line := strings.Repeat("a", 50) + "\n"
	for i := 0; i < 50; i++ {
		w.Write([]byte(line))
	}
	w.Close()

	// .1, .2, .3 should exist; .4 should not
	for i := 1; i <= 3; i++ {
		backup := fmt.Sprintf("%s.%d", path, i)
		if _, err := os.Stat(backup); err != nil {
			t.Errorf("backup .%d should exist", i)
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Error("backup .4 should NOT exist (maxBackups=3)")
	}
}

func TestNewCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.log")

	w, err := New(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := os.Stat(path); err != nil {
		t.Error("log file should be created on New()")
	}
}
