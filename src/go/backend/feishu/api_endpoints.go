package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// maxPageSize is the documented per-page cap for files.list.
const maxPageSize = 200

// maxEntriesPerFolder guards against runaway listings. Feishu's hard limit is
// 1500 per folder; we stop at 2000 so a broken tenant cannot loop forever.
const maxEntriesPerFolder = 2000

// remoteEntry is one item returned by files.list.
type remoteEntry struct {
	Name        string
	Token       string
	IsDir       bool
	ModTime     int64 // unix seconds, equals the upload time
	CreatedTime int64
	ParentToken string
	RawType     string
}

// listFilesReq is the wire shape of GET /open-apis/drive/v1/files.
type listFilesReq struct {
	Files         []fileJSON `json:"files"`
	HasMore       bool       `json:"has_more"`
	NextPageToken string     `json:"next_page_token"`
}

type fileJSON struct {
	Name         string `json:"name"`
	Token        string `json:"token"`
	Type         string `json:"type"`
	ParentToken  string `json:"parent_token"`
	CreatedTime  string `json:"created_time"`
	ModifiedTime string `json:"modified_time"`
	URL          string `json:"url"`
}

// listFolder returns every direct child of folderToken, following pagination.
func (c *client) listFolder(ctx context.Context, folderToken string) ([]remoteEntry, error) {
	const where = "files.list"

	var (
		out       []remoteEntry
		pageToken string
	)

	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("folder_token", folderToken)
		q.Set("page_size", fmt.Sprint(maxPageSize))
		if pageToken != "" {
			q.Set("page_token", pageToken)
		}

		res, err := c.do(ctx, request{
			where:  where,
			method: http.MethodGet,
			path:   "/open-apis/drive/v1/files",
			query:  q,
		})
		if err != nil {
			return nil, err
		}

		var payload listFilesReq
		if err := json.Unmarshal(res.Data, &payload); err != nil {
			return nil, fmt.Errorf("%s: decode page %d: %w", where, page, err)
		}

		for _, f := range payload.Files {
			out = append(out, remoteEntry{
				Name:        f.Name,
				Token:       f.Token,
				IsDir:       f.Type == "folder",
				ModTime:     parseInt64(f.ModifiedTime),
				CreatedTime: parseInt64(f.CreatedTime),
				ParentToken: f.ParentToken,
				RawType:     f.Type,
			})
		}

		if len(out) > maxEntriesPerFolder {
			return nil, &APIError{
				Code:  CodeTooManyNodes,
				Msg:   fmt.Sprintf("folder has more than %d entries", maxEntriesPerFolder),
				Where: where,
			}
		}

		if !payload.HasMore || payload.NextPageToken == "" {
			return out, nil
		}
		pageToken = payload.NextPageToken

		// Defensive: a server that keeps returning has_more with a repeating
		// token would otherwise spin forever.
		if page > 1000 {
			return nil, fmt.Errorf("%s: pagination did not terminate after 1000 pages", where)
		}
	}
}

// folderMeta fetches title/owner for a folder token. Used to validate the root
// at NewFs time and to produce readable errors.
type folderMeta struct {
	Token   string `json:"doc_token"`
	Title   string `json:"title"`
	OwnerID string `json:"owner_id"`
	URL     string `json:"url"`
}

func (c *client) getFolderMeta(ctx context.Context, token string) (*folderMeta, error) {
	const where = "metas.batch_query"

	body, err := json.Marshal(map[string]any{
		"request_docs": []map[string]string{{"doc_token": token, "doc_type": "folder"}},
		"with_url":     true,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", where, err)
	}

	res, err := c.do(ctx, request{
		where:       where,
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/metas/batch_query",
		body:        body,
		contentType: "application/json; charset=utf-8",
	})
	if err != nil {
		return nil, err
	}

	var payload struct {
		Metas []folderMeta `json:"metas"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", where, err)
	}
	if len(payload.Metas) == 0 {
		return nil, &APIError{
			Code:  CodeParentNotFound,
			Msg:   "folder token did not resolve to any document",
			Where: where,
		}
	}
	return &payload.Metas[0], nil
}

// createFolder creates a sub-folder and returns its token.
//
// Requires the space:folder:create scope (verified by M0 probing).
func (c *client) createFolder(ctx context.Context, parentToken, name string) (string, error) {
	const where = "files.create_folder"

	body, err := json.Marshal(map[string]string{"name": name, "folder_token": parentToken})
	if err != nil {
		return "", fmt.Errorf("%s: encode request: %w", where, err)
	}

	res, err := c.do(ctx, request{
		where:       where,
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/files/create_folder",
		body:        body,
		contentType: "application/json; charset=utf-8",
	})
	if err != nil {
		return "", err
	}

	var payload struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		return "", fmt.Errorf("%s: decode: %w", where, err)
	}
	if payload.Token == "" {
		return "", fmt.Errorf("%s: server returned an empty folder token", where)
	}
	return payload.Token, nil
}

// uploadAll uploads a file that fits in one request (<= 20MB).
func (c *client) uploadAll(ctx context.Context, parentToken, name string, content []byte) (string, error) {
	const where = "files.upload_all"

	if len(content) > MaxDirectUpload {
		return "", &ErrFileTooLarge{Name: name, Size: int64(len(content))}
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fields := [][2]string{
		{"file_name", name},
		{"parent_type", "explorer"},
		{"parent_node", parentToken},
		{"size", fmt.Sprint(len(content))},
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return "", fmt.Errorf("%s: write field %s: %w", where, f[0], err)
		}
	}

	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return "", fmt.Errorf("%s: create form file: %w", where, err)
	}
	if _, err := part.Write(content); err != nil {
		return "", fmt.Errorf("%s: write content: %w", where, err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("%s: finalize multipart: %w", where, err)
	}

	res, err := c.do(ctx, request{
		where:       where,
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/files/upload_all",
		body:        buf.Bytes(),
		contentType: mw.FormDataContentType(),
	})
	if err != nil {
		return "", err
	}

	var payload struct {
		FileToken string `json:"file_token"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		return "", fmt.Errorf("%s: decode: %w", where, err)
	}
	if payload.FileToken == "" {
		return "", fmt.Errorf("%s: server returned an empty file token", where)
	}
	return payload.FileToken, nil
}

// deleteFile removes a file by token.
//
// Irreversible: Feishu exposes no trash endpoint to the tenant token
// (verified during M0, see PLAN.md §3.3).
func (c *client) deleteFile(ctx context.Context, token string) error {
	const where = "files.delete"

	q := url.Values{}
	q.Set("type", "file")

	_, err := c.do(ctx, request{
		where:  where,
		method: http.MethodDelete,
		path:   "/open-apis/drive/v1/files/" + url.PathEscape(token),
		query:  q,
	})
	return err
}

// uploadPrepare starts a block upload and returns the server's split strategy.
//
// The server decides block_size; the client must not assume 4MB even though
// that is today's value (docs/decisions.md D21).
func (c *client) uploadPrepare(ctx context.Context, parentToken, name string, size int64) (uploadID string, blockSize int64, blockNum int, err error) {
	const where = "files.upload_prepare"

	body, err := json.Marshal(struct {
		FileName   string `json:"file_name"`
		ParentType string `json:"parent_type"`
		ParentNode string `json:"parent_node"`
		Size       int64  `json:"size"`
	}{
		FileName:   name,
		ParentType: "explorer",
		ParentNode: parentToken,
		Size:       size,
	})
	if err != nil {
		return "", 0, 0, fmt.Errorf("%s: encode request: %w", where, err)
	}

	res, err := c.do(ctx, request{
		where:       where,
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/files/upload_prepare",
		body:        body,
		contentType: "application/json; charset=utf-8",
	})
	if err != nil {
		return "", 0, 0, err
	}

	var payload struct {
		UploadID  string `json:"upload_id"`
		BlockSize int64  `json:"block_size"`
		BlockNum  int    `json:"block_num"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		return "", 0, 0, fmt.Errorf("%s: decode: %w", where, err)
	}
	if payload.UploadID == "" {
		return "", 0, 0, fmt.Errorf("%s: server returned an empty upload_id", where)
	}
	if payload.BlockSize <= 0 {
		return "", 0, 0, fmt.Errorf("%s: server returned an unusable block_size %d", where, payload.BlockSize)
	}
	return payload.UploadID, payload.BlockSize, payload.BlockNum, nil
}

// uploadPart sends one block. seq starts at 0.
//
// The checksum field is optional and was verified unnecessary during M0, so it
// is not sent: computing Adler-32 over every block costs time and adds a code
// path that could disagree with the server's definition.
func (c *client) uploadPart(ctx context.Context, uploadID string, seq int, block []byte) error {
	const where = "files.upload_part"

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fields := [][2]string{
		{"upload_id", uploadID},
		{"seq", fmt.Sprint(seq)},
		{"size", fmt.Sprint(len(block))},
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return fmt.Errorf("%s: write field %s: %w", where, f[0], err)
		}
	}

	part, err := mw.CreateFormFile("file", "block")
	if err != nil {
		return fmt.Errorf("%s: create form file: %w", where, err)
	}
	if _, err := part.Write(block); err != nil {
		return fmt.Errorf("%s: write block %d: %w", where, seq, err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("%s: finalize multipart: %w", where, err)
	}

	_, err = c.do(ctx, request{
		where:       where,
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/files/upload_part",
		body:        buf.Bytes(),
		contentType: mw.FormDataContentType(),
	})
	if err != nil {
		return fmt.Errorf("%s: block %d (%d bytes): %w", where, seq, len(block), err)
	}
	return nil
}

// uploadFinish completes a block upload and returns the new file token.
func (c *client) uploadFinish(ctx context.Context, uploadID string, blockNum int) (string, error) {
	const where = "files.upload_finish"

	body, err := json.Marshal(struct {
		UploadID string `json:"upload_id"`
		BlockNum int    `json:"block_num"`
	}{UploadID: uploadID, BlockNum: blockNum})
	if err != nil {
		return "", fmt.Errorf("%s: encode request: %w", where, err)
	}

	res, err := c.do(ctx, request{
		where:       where,
		method:      http.MethodPost,
		path:        "/open-apis/drive/v1/files/upload_finish",
		body:        body,
		contentType: "application/json; charset=utf-8",
	})
	if err != nil {
		return "", err
	}

	var payload struct {
		FileToken string `json:"file_token"`
	}
	if err := json.Unmarshal(res.Data, &payload); err != nil {
		return "", fmt.Errorf("%s: decode: %w", where, err)
	}
	if payload.FileToken == "" {
		return "", fmt.Errorf("%s: server returned an empty file token", where)
	}
	return payload.FileToken, nil
}

// deleteFolder removes a folder by token.
func (c *client) deleteFolder(ctx context.Context, token string) error {
	const where = "files.delete_folder"

	q := url.Values{}
	q.Set("type", "folder")

	_, err := c.do(ctx, request{
		where:  where,
		method: http.MethodDelete,
		path:   "/open-apis/drive/v1/files/" + url.PathEscape(token),
		query:  q,
	})
	return err
}

// downloadBody is an open download stream plus whether the server actually
// honoured the requested range.
//
// The caller needs that flag: a 206 body already holds only the requested
// window, so skipping to the offset again would consume it entirely.
type downloadBody struct {
	Body    io.ReadCloser
	Partial bool
}

// downloadStream opens file content as a stream, honouring a byte range when
// supplied.
//
// Feishu answers ranged requests with 206 + Content-Range (verified by M0).
//
// This deliberately does NOT go through client.do: that path buffers the whole
// response through an 8MB limiter sized for JSON listings, which silently
// truncated every download larger than 8MB (found against the real tenant: files
// of 20MB, 21MB and 30MB all arrived as exactly 8388608 bytes). Streaming also
// means a 1GB file does not need 1GB of RAM.
//
// Retries stop at the first response that carries a body: replaying a request
// after part of the stream was consumed would corrupt the output.
func (c *client) downloadStream(ctx context.Context, token, rangeHeader string) (*downloadBody, error) {
	const where = "files.download"

	var (
		lastErr      error
		backoff      = initialBackoff
		tokenRetried bool
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, partial, err := c.openDownload(ctx, token, rangeHeader)
		if err == nil {
			return &downloadBody{Body: body, Partial: partial}, nil
		}
		lastErr = err

		switch classify(err) {
		case classTokenInvalid:
			if tokenRetried {
				return nil, err
			}
			tokenRetried = true
			c.ts.Invalidate()
			continue
		case classRetryable:
			if attempt == maxAttempts {
				return nil, fmt.Errorf("%s: giving up after %d attempts: %w", where, attempt, err)
			}
			c.sleep(c.jitter(backoff))
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		default:
			return nil, err
		}
	}
	return nil, lastErr
}

// openDownload performs one download attempt and returns the live body, plus
// whether the response is a partial one.
func (c *client) openDownload(ctx context.Context, token, rangeHeader string) (io.ReadCloser, bool, error) {
	const where = "files.download"

	tok, err := c.ts.Token(ctx)
	if err != nil {
		return nil, false, err
	}

	u := c.baseURL + "/open-apis/drive/v1/files/" + url.PathEscape(token) + "/download"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, fmt.Errorf("%s: build request: %w", where, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, &retryableTransportError{where: where, err: err}
	}

	// Success: hand the caller the body and let it stream.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return resp.Body, resp.StatusCode == http.StatusPartialContent, nil
	}

	// Failure: the envelope is small, so bounded reading is correct here.
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if readErr != nil {
		return nil, false, &retryableTransportError{where: where, err: readErr}
	}

	var env envelope
	if jsonErr := json.Unmarshal(raw, &env); jsonErr != nil {
		return nil, false, c.httpStatusError(where, resp.StatusCode, raw, jsonErr)
	}
	return nil, false, c.businessError(where, env.Code, env.Msg, raw, resp.StatusCode)
}

// parseInt64 tolerates both numeric and string timestamps.
func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			// Feishu sometimes returns "1793783897" style strings; anything
			// else is unexpected and treated as unknown.
			return 0
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

// contentTypeFor guesses the upload content type from the file name.
func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}
