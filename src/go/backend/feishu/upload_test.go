package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// blockServer is a fake Feishu that implements the three-step block protocol and
// reassembles the blocks so tests can compare them with the original bytes.
type blockServer struct {
	blockSize int64
	// blockNum lets a test make the server disagree with our own arithmetic.
	blockNum int

	blocks   map[int][]byte
	order    []int
	finished bool
	calls    []string
	total    int64
}

func newBlockServer(blockSize int64, blockNum int) *blockServer {
	return &blockServer{blockSize: blockSize, blockNum: blockNum, blocks: map[int][]byte{}}
}

func (b *blockServer) reassembled() []byte {
	var out bytes.Buffer
	for _, seq := range b.order {
		out.Write(b.blocks[seq])
	}
	return out.Bytes()
}

func (b *blockServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/files/upload_prepare"):
			b.calls = append(b.calls, "prepare")
			var req struct {
				FileName string `json:"file_name"`
				Size     int64  `json:"size"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			b.total = req.Size

			num := b.blockNum
			if num == 0 {
				num = int((req.Size + b.blockSize - 1) / b.blockSize)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"upload_id":  "up-1",
					"block_size": b.blockSize,
					"block_num":  num,
				},
			})

		case strings.HasSuffix(r.URL.Path, "/files/upload_part"):
			b.calls = append(b.calls, "part")
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				t.Errorf("parse multipart: %v", err)
				return
			}
			seq, err := strconv.Atoi(r.FormValue("seq"))
			if err != nil {
				t.Errorf("seq %q is not an integer: %v", r.FormValue("seq"), err)
				return
			}
			fh, _, err := r.FormFile("file")
			if err != nil {
				t.Errorf("missing file part: %v", err)
				return
			}
			defer func() { _ = fh.Close() }()
			data, err := io.ReadAll(fh)
			if err != nil {
				t.Errorf("read block: %v", err)
				return
			}
			declared, err := strconv.ParseInt(r.FormValue("size"), 10, 64)
			if err != nil || declared != int64(len(data)) {
				t.Errorf("block %d: declared size %q but body is %d bytes",
					seq, r.FormValue("size"), len(data))
			}
			b.blocks[seq] = data
			b.order = append(b.order, seq)
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))

		case strings.HasSuffix(r.URL.Path, "/files/upload_finish"):
			b.calls = append(b.calls, "finish")
			b.finished = true
			var req struct {
				UploadID string `json:"upload_id"`
				BlockNum int    `json:"block_num"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(b.blocks) != req.BlockNum {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": CodeBlockMissing,
					"msg":  "block missing",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"file_token": "file-token-1"},
			})

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}
}

func chunkTestFs(t *testing.T, b *blockServer) *Fs {
	t.Helper()
	c, _ := testClient(t, b.handler(t))
	return &Fs{name: "test", client: c}
}

// content builds n bytes with a position-dependent pattern, so a misordered or
// truncated block cannot accidentally match.
func content(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i*7 + i/251)
	}
	return buf
}

// F2 core: a file larger than one block is split exactly as the server asked and
// reassembles to the original bytes.
func TestChunkedUploadReassembles(t *testing.T) {
	const blockSize = 1024
	for _, size := range []int{
		blockSize + 1,   // one full block + 1 byte
		3 * blockSize,   // exact multiple
		3*blockSize + 7, // ragged tail
	} {
		t.Run(fmt.Sprintf("%d-bytes", size), func(t *testing.T) {
			b := newBlockServer(blockSize, 0)
			f := chunkTestFs(t, b)

			original := content(size)
			token, err := f.putChunked(context.Background(), bytes.NewReader(original),
				"big.bin", "big.bin", "parent-token", int64(size))
			if err != nil {
				t.Fatalf("putChunked: %v", err)
			}
			if token != "file-token-1" {
				t.Errorf("token = %q", token)
			}
			if got := b.reassembled(); !bytes.Equal(got, original) {
				t.Errorf("reassembled content differs: got %d bytes, want %d", len(got), len(original))
			}

			// Sequentially, starting at 0, with no gaps.
			wantBlocks := int((int64(size) + blockSize - 1) / blockSize)
			if len(b.order) != wantBlocks {
				t.Fatalf("uploaded %d blocks, want %d", len(b.order), wantBlocks)
			}
			for i, seq := range b.order {
				if seq != i {
					t.Errorf("block order[%d] = %d, want %d", i, seq, i)
				}
			}

			// Every block except possibly the last must be exactly blockSize.
			for seq, data := range b.blocks {
				last := seq == wantBlocks-1
				if !last && int64(len(data)) != blockSize {
					t.Errorf("block %d has %d bytes, want %d", seq, len(data), blockSize)
				}
				if int64(len(data)) > blockSize {
					t.Errorf("block %d is oversized: %d", seq, len(data))
				}
			}

			if !b.finished {
				t.Error("upload_finish was never called")
			}
		})
	}
}

// The server's block_size is the authority; a block_num that contradicts it
// means the contract changed and guessing would upload data nothing commits.
func TestChunkedUploadRejectsInconsistentBlockNum(t *testing.T) {
	b := newBlockServer(1024, 99) // nonsense for a 3000-byte file
	f := chunkTestFs(t, b)

	_, err := f.putChunked(context.Background(), bytes.NewReader(content(3000)),
		"big.bin", "big.bin", "parent", 3000)
	if err == nil {
		t.Fatal("expected an error when block_num disagrees with block_size")
	}
	if !strings.Contains(err.Error(), "block_num=99") {
		t.Errorf("error should quote the server's number\ngot: %v", err)
	}
	if len(b.calls) != 1 || b.calls[0] != "prepare" {
		t.Errorf("no block should be uploaded after the mismatch; calls = %v", b.calls)
	}
}

// A source that ends early must abort before upload_finish: committing a short
// file would look like success while silently truncating data.
func TestChunkedUploadDetectsShrunkSource(t *testing.T) {
	const blockSize = 1024
	b := newBlockServer(blockSize, 0)
	f := chunkTestFs(t, b)

	// Declare 4 blocks but supply only 2.5 blocks.
	_, err := f.putChunked(context.Background(), bytes.NewReader(content(blockSize*2+512)),
		"big.bin", "big.bin", "parent", blockSize*4)
	if err == nil {
		t.Fatal("expected an error for a source shorter than its declared size")
	}
	if !strings.Contains(err.Error(), "shrank") {
		t.Errorf("error should say the file shrank\ngot: %v", err)
	}
	if b.finished {
		t.Error("upload_finish must not be called for a truncated source")
	}
}

// A source longer than declared must also abort: the extra bytes would be
// dropped without a word.
func TestChunkedUploadDetectsGrownSource(t *testing.T) {
	const blockSize = 1024
	b := newBlockServer(blockSize, 0)
	f := chunkTestFs(t, b)

	_, err := f.putChunked(context.Background(), bytes.NewReader(content(blockSize*2)),
		"big.bin", "big.bin", "parent", blockSize+10)
	if err == nil {
		t.Fatal("expected an error for a source longer than its declared size")
	}
	if !strings.Contains(err.Error(), "grew") {
		t.Errorf("error should say the file grew\ngot: %v", err)
	}
	if b.finished {
		t.Error("upload_finish must not be called when extra bytes remain")
	}
}

// A failing block must stop the upload rather than keep sending the rest.
func TestChunkedUploadStopsOnBlockFailure(t *testing.T) {
	const blockSize = 1024
	var parts int
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/files/upload_prepare"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"upload_id": "up", "block_size": blockSize, "block_num": 3},
			})
		case strings.HasSuffix(r.URL.Path, "/files/upload_part"):
			parts++
			if parts == 2 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": CodeForbidden, "msg": "forbidden",
				})
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			t.Errorf("unexpected call %s", r.URL.Path)
		}
	})
	f := &Fs{name: "test", client: c}

	_, err := f.putChunked(context.Background(), bytes.NewReader(content(blockSize*3)),
		"big.bin", "big.bin", "parent", blockSize*3)
	if err == nil {
		t.Fatal("expected the block failure to surface")
	}
	if !strings.Contains(err.Error(), "block 2/3") {
		t.Errorf("error should name the failing block\ngot: %v", err)
	}
	if parts != 2 {
		t.Errorf("upload continued after a failure: %d block calls", parts)
	}
}
