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
	// CodeNotFound is returned when a token no longer exists.
	CodeNotFound = 1061003
	// CodeUploadIDExpired means the 24-hour window of a block upload closed.
	CodeUploadIDExpired = 1061021
	// CodeFileSizeBeyondLimit means the file exceeds Feishu's per-file cap
	// (the tenant plan decides the number; it is not the 20MB request cap).
	CodeFileSizeBeyondLimit = 1061043
	// CodeTenantQuotaExceeded / CodeUserQuotaExceeded mean the drive is full.
	CodeTenantQuotaExceeded = 1061101
	CodeUserQuotaExceeded   = 1061061
	// CodeBlockMissing means upload_finish saw fewer blocks than declared.
	CodeBlockMissing = 1062010
	// CodeBlockNumOutOfBounds means the server rejected a block sequence number.
	CodeBlockNumOutOfBounds = 1062011
	// CodeMigrating is a temporary tenant-wide lock during a data migration.
	CodeMigrating = 1064230
	// Codes from the move endpoint, which has its own permission model: the
	// caller needs manage rights on the node plus edit rights on both the source
	// and the destination folder.
	CodeFileDeleted       = 1061007 // the node was deleted while we held its token
	CodeSourceNoPerm      = 1062524 // no edit permission on the source folder
	CodeDestinationNoPerm = 1062535 // no edit permission on the destination folder
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
	Scopes []string // scopes that would each be sufficient
	AppID  string
	Where  string
	RawMsg string
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
	Code    int
	Msg     string
	Where   string
	LogID   string
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

// ErrFileTooLarge is returned when a source announces a size that does not
// match what it actually produced, so the upload cannot be planned.
//
// Ordinary large files no longer reach this error: they go through the block
// protocol (upload.go). This case is a source whose Size() claims <= 20MB (or is
// negative) while the stream turns out to be bigger, which leaves no trustworthy
// byte count for upload_prepare.
type ErrFileTooLarge struct {
	Name string
	Size int64
	// SizeUnknown marks the "announced size was wrong" case, which needs a
	// different explanation from a plain "too big".
	SizeUnknown bool
}

// MaxDirectUpload is the upload_all cap observed by probing (21MB was rejected
// with code 1061002). Larger files use upload_prepare/upload_part/upload_finish.
const MaxDirectUpload = 20 * 1024 * 1024

func (e *ErrFileTooLarge) Error() string {
	if e.SizeUnknown {
		return fmt.Sprintf(
			"file %q is larger than the 20MB single-request limit (%s) but its declared size "+
				"does not say so, and the chunked upload path needs an exact size before it starts. "+
				"Refusing to guess.",
			e.Name, humanSize(e.Size))
	}
	return fmt.Sprintf(
		"file %q (%s) exceeds the Feishu single-request upload limit of 20MB.",
		e.Name, humanSize(e.Size))
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
	CodeTokenInvalid:        {class: classTokenInvalid},
	CodeForbidden:           {class: classPermission},
	CodeNotFound:            {class: classNotFound},
	CodeParentNotFound:      {class: classNotFound},
	CodeFileDeleted:         {class: classNotFound},
	CodeSourceNoPerm:        {class: classPermission},
	CodeDestinationNoPerm:   {class: classPermission},
	CodeParamsError:         {class: classInvalidArgument},
	CodeDepthExceeded:       {class: classInvalidArgument},
	CodeTooManyNodes:        {class: classInvalidArgument},
	CodeFileSizeBeyondLimit: {class: classInvalidArgument},
	CodeTenantQuotaExceeded: {class: classInvalidArgument},
	CodeUserQuotaExceeded:   {class: classInvalidArgument},
	CodeUploadIDExpired:     {class: classInvalidArgument},
	CodeBlockMissing:        {class: classOther},
	CodeBlockNumOutOfBounds: {class: classOther},
	CodeRetryable:           {retryable: true, class: classRetryable},
	CodeSizeMismatch:        {retryable: true, class: classRetryable},
	CodeInternal:            {retryable: true, class: classRetryable},
	CodeMigrating:           {retryable: true, class: classRetryable},
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
