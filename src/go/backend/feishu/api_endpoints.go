package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	Name         string
	Token        string
	IsDir        bool
	ModTime      int64 // unix seconds, equals the upload time
	CreatedTime  int64
	ParentToken  string
	RawType      string
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

// download fetches file content, honouring a byte range when supplied.
//
// Feishu answers ranged requests with 206 + Content-Range (verified by M0).
func (c *client) download(ctx context.Context, token string, rangeHeader string) (*result, error) {
	const where = "files.download"

	headers := map[string]string{}
	if rangeHeader != "" {
		headers["Range"] = rangeHeader
	}

	return c.do(ctx, request{
		where:        where,
		method:       http.MethodGet,
		path:         "/open-apis/drive/v1/files/" + url.PathEscape(token) + "/download",
		rawResponse:  true,
		extraHeaders: headers,
	})
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
