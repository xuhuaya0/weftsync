package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
)

// objectWithContent returns an Object whose Open() talks to a server that serves
// payload, optionally honouring Range.
func objectWithContent(t *testing.T, payload []byte, honourRange bool) *Object {
	t.Helper()
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			w.Header().Set("Content-Type", "application/octet-stream")

			if honourRange {
				if rng := r.Header.Get("Range"); rng != "" {
					start, end, ok := parseRange(rng, int64(len(payload)))
					if ok {
						w.Header().Set("Content-Range",
							fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
						w.WriteHeader(http.StatusPartialContent)
						_, _ = w.Write(payload[start : end+1])
						return
					}
				}
			}
			// Either not honouring Range, or no Range header: send everything.
			_, _ = w.Write(payload)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": CodeForbidden, "msg": "forbidden"})
	})

	f := &Fs{name: "test", client: c}
	return &Object{fs: f, remote: "file.bin", entry: remoteEntry{Token: "tok"}}
}

func parseRange(header string, size int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(header, "bytes=")
	if !found {
		return 0, 0, false
	}
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	if a == "" {
		// suffix range: last N bytes
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end = size - 1
	if b != "" {
		if end, err = strconv.ParseInt(b, 10, 64); err != nil {
			return 0, 0, false
		}
	}
	if start > end || start >= size {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

// Regression for a real defect: downloads were buffered through the 8MB cap
// meant for JSON listings, so every file larger than 8MB arrived silently
// truncated. Three files (20MB, 21MB, 30MB) all came back as exactly 8388608
// bytes against the live tenant.
func TestOpenDoesNotTruncateLargeFiles(t *testing.T) {
	const size = 24 * 1024 * 1024 // well past the old 8MB limit
	payload := content(size)
	o := objectWithContent(t, payload, true)

	rc, err := o.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != size {
		t.Fatalf("read %d bytes, want %d — a short read is silent data loss", len(got), size)
	}
	if !bytes.Equal(got, payload) {
		t.Error("content differs from the source")
	}
}

// A ranged Open must return exactly the requested window.
func TestOpenHonoursRange(t *testing.T) {
	payload := content(4096)
	o := objectWithContent(t, payload, true)

	rc, err := o.Open(context.Background(), &fs.RangeOption{Start: 1000, End: 1999})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload[1000:2000]) {
		t.Errorf("got %d bytes, want bytes 1000..1999", len(got))
	}
}

// If the server ignores Range and sends the whole file, the caller must still
// receive only the window it asked for.
func TestOpenSkipsWhenServerIgnoresRange(t *testing.T) {
	payload := content(4096)
	o := objectWithContent(t, payload, false)

	rc, err := o.Open(context.Background(), &fs.RangeOption{Start: 1000, End: 1999})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload[1000:2000]) {
		t.Errorf("got %d bytes, want bytes 1000..1999", len(got))
	}
}

// A JSON error envelope on the download endpoint must surface as a typed error,
// not as file content.
func TestOpenSurfacesAPIError(t *testing.T) {
	o := objectWithContent(t, []byte("ignored"), true)
	// Point at a non-existent token: the test server answers with a forbidden
	// envelope for anything that is not /download.
	o.entry.Token = "tok"

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": CodeForbidden, "msg": "forbidden"})
	})
	o.fs = &Fs{name: "test", client: c}

	_, err := o.Open(context.Background())
	if err == nil {
		t.Fatal("expected an error for a failing download")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeForbidden {
		t.Errorf("error = %v, want APIError with code %d", err, CodeForbidden)
	}
}
