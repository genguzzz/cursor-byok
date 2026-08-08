package h2agentproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type captureStore struct {
	dir string
	mu  sync.Mutex
	seq int
}

func newCaptureStore(dir string) (*captureStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &captureStore{dir: dir}, nil
}

func (store *captureStore) nextID() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.seq++
	return store.seq
}

func (store *captureStore) writeJSON(name string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(store.dir, name), payload, 0o644)
}

func (store *captureStore) createBodyFile(name string) (*os.File, error) {
	return os.OpenFile(filepath.Join(store.dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
}

func requestCaptureName(id int, kind string) string {
	return fmt.Sprintf("%02d_request.%s", id, kind)
}

func responseCaptureName(id int, kind string) string {
	return fmt.Sprintf("%02d_response.%s", id, kind)
}

func flattenHeaders(header http.Header) map[string]string {
	out := make(map[string]string, len(header))
	for key, values := range header {
		joined := strings.Join(values, ", ")
		if isSensitiveHeader(key) && joined != "" {
			out[key] = "<redacted>"
			continue
		}
		out[key] = joined
	}
	return out
}

func isSensitiveHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key", "x-cursor-checksum":
		return true
	default:
		return false
	}
}

type teeReadCloser struct {
	reader io.ReadCloser
	writer io.WriteCloser
	once   sync.Once
	closed chan struct{}
	size   int64
	mu     sync.Mutex
}

func newTeeReadCloser(reader io.ReadCloser, writer io.WriteCloser) *teeReadCloser {
	return &teeReadCloser{
		reader: reader,
		writer: writer,
		closed: make(chan struct{}),
	}
}

func (tee *teeReadCloser) Read(buffer []byte) (int, error) {
	n, err := tee.reader.Read(buffer)
	if n > 0 && tee.writer != nil {
		if _, writeErr := tee.writer.Write(buffer[:n]); writeErr != nil {
			return n, writeErr
		}
		tee.mu.Lock()
		tee.size += int64(n)
		tee.mu.Unlock()
	}
	if err == io.EOF {
		tee.closeWriter()
	}
	return n, err
}

func (tee *teeReadCloser) Close() error {
	tee.closeWriter()
	if tee.reader == nil {
		return nil
	}
	return tee.reader.Close()
}

func (tee *teeReadCloser) Size() int64 {
	tee.mu.Lock()
	defer tee.mu.Unlock()
	return tee.size
}

func (tee *teeReadCloser) closeWriter() {
	tee.once.Do(func() {
		if tee.writer != nil {
			_ = tee.writer.Close()
		}
		close(tee.closed)
	})
}

func (tee *teeReadCloser) Wait(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-tee.closed:
		return true
	case <-timer.C:
		return false
	}
}
