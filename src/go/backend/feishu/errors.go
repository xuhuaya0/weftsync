package feishu

import (
	"errors"
	"fmt"
)

// Feishu error codes observed during M0 probing and documented officially.
// See specs/001-feishu-read-upload/contracts/error-codes.md.
const (
	CodeMissingScope   = 99991672 // required scope not granted to the app
	CodeTokenInvalid   = 99991663 // tenant_access_token invalid or expired
	CodeForbidden      = 1061004  // no permission on this node
	CodeParentNotFound = 1061044  // parent_node does not exist
	CodeRetryable      = 1061045  // transient, safe to retry
	CodeParamsError    = 1061002  // invalid argument (also used for >20MB)
	CodeSizeMismatch   = 1062009  // uploaded size differs from declared size
	CodeDepthExceeded  = 1062506  // folder depth limit exceeded
	CodeTooManyNodes   = 1062507  // per-folder node limit exceeded
	// CodeInternal is what files.delete returned intermittently during real
	// runs ("unknown error"). The same delete succeeds on retry, so it is a
	// transient condition rather than a permanent failure.
	CodeInternal = 1061001
)

// APIError is a Feishu business-level error (HTTP 200 with code != 0, or an
// HTTP error whose body carried a Feishu code).
type APIError struct {
	Code  int
	Msg   string
	Where string // which call produced it, e.g. "files.list"
	LogID string // Feishu log id, useful when contacting support
}

func (e *APIError) Error() string {
	if e.Where != "" {
		return fmt.Sprintf("feishu %s: code=%d msg=%s", e.Where, e.Code, e.Msg)
	}
	return fmt.Sprintf("feishu: code=%d msg=%s", e.Code, e.Msg)
}

// Retryable reports whether the call may succeed if repeated.
func (e *APIError) Retryable() bool {
	return codeTable[e.Code].retryable
}

// MissingScopeError means the application lacks a required Drive scope.
//
// The message always names the scope and includes the console link, because
// that is the only actionable information for the user (FR-022 / FR-024).
type MissingScopeError struct {
	Scopes  []string // scopes that would each be sufficient
	AppID   string
	Where   string
	RawMsg  string
}

func (e *MissingScopeError) Error() string {
	msg := fmt.Sprintf("the Feishu app is missing a required Drive scope (needed for %s).", e.Where)
	if len(e.Scopes) > 0 {
		msg += " Any one of these is sufficient: " + joinScopes(e.Scopes) + "."
	}
	if e.AppID != "" {
		msg += " Grant it at https://open.feishu.cn/app/" + e.AppID +
			"/auth?q=" + joinScopes(e.Scopes) +
			"&op_from=openapi&token_type=tenant" +
			" and then publish a new app version."
	}
	return msg
}

func joinScopes(scopes []string) string {
	out := ""
	for i, s := range scopes {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// UncategorizedError wraps an error whose cause is not yet known.
type UncategorizedError struct {
	Code   int
	Msg    string
	Where  string
	LogID  string
	Wrapped error
}

func (e *UncategorizedError) Error() string {
	s := fmt.Sprintf("feishu returned an unexpected error (code=%d) from %s: %s", e.Code, e.Where, e.Msg)
	if e.LogID != "" {
		s += " [log_id=" + e.LogID + "]"
	}
	return s
}

func (e *UncategorizedError) Unwrap() error { return e.Wrapped }

// ErrFileTooLarge is returned by Put when the source exceeds the 20MB direct
// upload cap (FR-013). The message must contain "20MB" (SC-004).
type ErrFileTooLarge struct {
	Name string
	Size int64
}

// MaxDirectUpload is the upload_all cap observed by probing (21MB was rejected
// with code 1061002).
const MaxDirectUpload = 20 * 1024 * 1024

func (e *ErrFileTooLarge) Error() string {
	size := "larger than 20MB"
	if e.Size > 0 {
		size = fmt.Sprintf("at least %s", humanSize(e.Size))
	}
	return fmt.Sprintf(
		"file %q is %s, which exceeds the Feishu single-request upload limit of 20MB. "+
			"Chunked upload is not implemented yet (planned for F2), "+
			"so this file cannot be uploaded in the current version.",
		e.Name, size)
}

func humanSize(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ErrNoRootToken is returned when root_token is empty.
var ErrNoRootToken = errors.New("feishu: root_token is required " +
	"(a folder token, or a full folder URL to extract it from)")

// classification groups an error so callers can branch without string matching.
type classification int

const (
	classOther classification = iota
	classRetryable
	classTokenInvalid
	classPermission
	classNotFound
	classInvalidArgument
)

// codeInfo says how a known business code must be handled.
type codeInfo struct {
	retryable bool
	class     classification
}

// codeTable is the single place a business code gets its meaning.
//
// It drives three decisions that must agree: whether businessError turns the
// code into an APIError, whether APIError.Retryable reports true, and how the
// transport layer classifies it. Keeping three separate lists here previously
// caused a real bug: 1061001 was marked retryable in one list but absent from
// the constructor list, so it never became an APIError and was never retried.
var codeTable = map[int]codeInfo{
	CodeTokenInvalid:   {class: classTokenInvalid},
	CodeForbidden:      {class: classPermission},
	CodeParentNotFound: {class: classNotFound},
	CodeParamsError:    {class: classInvalidArgument},
	CodeDepthExceeded:  {class: classInvalidArgument},
	CodeTooManyNodes:   {class: classInvalidArgument},
	CodeRetryable:      {retryable: true, class: classRetryable},
	CodeSizeMismatch:   {retryable: true, class: classRetryable},
	CodeInternal:       {retryable: true, class: classRetryable},
}

// classify inspects err and reports how the transport layer should react.
func classify(err error) classification {
	if err == nil {
		return classOther
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return codeTable[apiErr.Code].class
	}

	var scopeErr *MissingScopeError
	if errors.As(err, &scopeErr) {
		return classPermission
	}

	var tooLarge *ErrFileTooLarge
	if errors.As(err, &tooLarge) {
		return classInvalidArgument
	}

	// Transport-level failures (connection reset, http 429/5xx) are retryable.
	var transportErr *retryableTransportError
	if errors.As(err, &transportErr) {
		return classRetryable
	}

	return classOther
}
