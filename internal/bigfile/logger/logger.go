package logger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/settings"
)

const (
	logFileName        = "novera-bigfile.log"
	rotatedLogFileName = "novera-bigfile.log.1"
	legacyLogFileName  = "quarry.log"
	defaultMaxBytes    = 1024 * 1024
)

var (
	mu       sync.Mutex
	maxBytes int64 = defaultMaxBytes
)

type Entry struct {
	Time      string            `json:"time"`
	Level     string            `json:"level"`
	Component string            `json:"component"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

func LogPath() (string, error) {
	dir, err := settings.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, logFileName), nil
}

func Warn(component string, message string, fields map[string]string) {
	_ = Write("warn", component, message, fields)
}

func Info(component string, message string, fields map[string]string) {
	_ = Write("info", component, message, fields)
}

func ReadRecent(limit int) ([]Entry, error) {
	if limit <= 0 {
		return nil, nil
	}
	mu.Lock()
	defer mu.Unlock()

	path, err := LogPath()
	if err != nil {
		return nil, err
	}
	// Legacy diagnostics are bounded and copied once. A failed best-effort
	// import must not make canonical diagnostics unavailable.
	_ = importLegacyLog(path)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	entries := make([]Entry, 0, limit)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
		if len(entries) > limit {
			copy(entries, entries[1:])
			entries = entries[:limit]
		}
	}
	if err := scanner.Err(); err != nil {
		return entries, err
	}
	reverseEntries(entries)
	return entries, nil
}

func reverseEntries(entries []Entry) {
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
}

func Write(level string, component string, message string, fields map[string]string) error {
	entry := Entry{
		Time:      time.Now().UTC().Format(time.RFC3339Nano),
		Level:     strings.TrimSpace(level),
		Component: strings.TrimSpace(component),
		Message:   strings.TrimSpace(message),
		Fields:    fields,
	}
	if entry.Level == "" {
		entry.Level = "info"
	}
	if entry.Component == "" {
		entry.Component = "app"
	}
	if entry.Message == "" {
		entry.Message = "event"
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	mu.Lock()
	defer mu.Unlock()

	path, err := LogPath()
	if err != nil {
		return err
	}
	// Preserve bounded historical diagnostics on first use without ever writing
	// back into the legacy product directory.
	_ = importLegacyLog(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rotateIfNeeded(path); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func importLegacyLog(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	legacyDir, available, err := settings.LegacyConfigDir()
	if err != nil || !available {
		return err
	}
	legacyPath := filepath.Join(legacyDir, legacyLogFileName)
	if pathsEqual(path, legacyPath) {
		return nil
	}
	legacyInfo, err := os.Lstat(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !legacyInfo.Mode().IsRegular() {
		return errors.New("legacy large-file log is not a regular file")
	}
	f, err := os.Open(legacyPath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("legacy large-file log is not a regular file")
	}
	start := info.Size() - defaultMaxBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, defaultMaxBytes+1))
	if err != nil {
		return err
	}
	if start > 0 {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			data = nil
		}
	}
	if len(data) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err = fileio.WriteFileAtomic(path, data, fileio.AtomicWriteOptions{
		Mode:       0o600,
		Overwrite:  false,
		TempSuffix: ".legacy-import.tmp",
	})
	if errors.Is(err, fileio.ErrExists) {
		return nil
	}
	return err
}

func pathsEqual(left string, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil {
		left = leftAbs
	}
	if rightErr == nil {
		right = rightAbs
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return left == right || (runtime.GOOS == "windows" && strings.EqualFold(left, right))
}

func rotateIfNeeded(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() <= maxBytes {
		return nil
	}
	rotatedPath := filepath.Join(filepath.Dir(path), rotatedLogFileName)
	if err := os.Remove(rotatedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(path, rotatedPath)
}
