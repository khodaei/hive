package logrotate

import (
	"fmt"
	"os"
	"sync"
)

// Writer is an io.Writer that rotates the underlying log file when it
// exceeds a configured size threshold.
type Writer struct {
	path       string
	maxBytes   int64
	maxBackups int

	mu   sync.Mutex
	file *os.File
	size int64
}

// New creates a new rotating log writer.
func New(path string, maxSizeMB int) (*Writer, error) {
	w := &Writer{
		path:       path,
		maxBytes:   int64(maxSizeMB) * 1024 * 1024,
		maxBackups: 3,
	}
	if err := w.openOrCreate(); err != nil {
		return nil, err
	}
	return w, nil
}

// Write implements io.Writer. Thread-safe.
func (w *Writer) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, fmt.Errorf("rotate: %w", err)
		}
	}

	n, err = w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

func (w *Writer) openOrCreate() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.size = info.Size()
	return nil
}

func (w *Writer) rotate() error {
	// Close current file
	if w.file != nil {
		w.file.Close()
	}

	// Shift existing backups: .3 -> delete, .2 -> .3, .1 -> .2, current -> .1
	for i := w.maxBackups; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		if i == w.maxBackups {
			os.Remove(src)
		} else {
			dst := fmt.Sprintf("%s.%d", w.path, i+1)
			os.Rename(src, dst)
		}
	}

	// Rename current to .1
	os.Rename(w.path, w.path+".1")

	// Open fresh file
	return w.openOrCreate()
}
