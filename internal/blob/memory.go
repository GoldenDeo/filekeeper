package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Memory is an in-memory Store for tests. Safe for concurrent use.
type Memory struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMemory() *Memory {
	return &Memory{data: map[string][]byte{}}
}

func (m *Memory) Put(_ context.Context, key string, r io.Reader, size int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return fmt.Errorf("memory put %s: size mismatch: got %d, declared %d", key, len(b), size)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	return nil
}

func (m *Memory) Get(_ context.Context, key string, rangeHeader string) (*Object, error) {
	m.mu.RLock()
	b, ok := m.data[key]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if rangeHeader == "" {
		return &Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b))}, nil
	}
	start, end, err := parseRange(rangeHeader, int64(len(b)))
	if err != nil {
		return nil, err
	}
	part := b[start : end+1]
	return &Object{
		Body:         io.NopCloser(bytes.NewReader(part)),
		Size:         int64(len(part)),
		ContentRange: fmt.Sprintf("bytes %d-%d/%d", start, end, len(b)),
	}, nil
}

func (m *Memory) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// Len reports the number of stored objects (test helper).
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.data)
}

// parseRange handles a single-range HTTP Range header: "bytes=a-b", "bytes=a-",
// "bytes=-n". Returns inclusive [start, end].
func parseRange(h string, size int64) (start, end int64, err error) {
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0, 0, fmt.Errorf("unsupported range %q", h)
	}
	from, to, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, fmt.Errorf("bad range %q", h)
	}
	switch {
	case from == "" && to != "": // suffix: last n bytes
		n, err := strconv.ParseInt(to, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, fmt.Errorf("bad range %q", h)
		}
		n = min(n, size)
		return size - n, size - 1, nil
	case from != "":
		start, err = strconv.ParseInt(from, 10, 64)
		if err != nil || start < 0 || start >= size {
			return 0, 0, fmt.Errorf("bad range %q", h)
		}
		end = size - 1
		if to != "" {
			end, err = strconv.ParseInt(to, 10, 64)
			if err != nil || end < start {
				return 0, 0, fmt.Errorf("bad range %q", h)
			}
			end = min(end, size-1)
		}
		return start, end, nil
	}
	return 0, 0, fmt.Errorf("bad range %q", h)
}
