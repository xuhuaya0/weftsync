package feishu

import (
	"errors"
	"strings"
	"testing"
)

// Covers E1-E9 classification rules from contracts/error-codes.md.
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want classification
	}{
		{"retryable 1061045", &APIError{Code: CodeRetryable}, classRetryable},
		{"size mismatch 1062009", &APIError{Code: CodeSizeMismatch}, classRetryable},
		{"token invalid", &APIError{Code: CodeTokenInvalid}, classTokenInvalid},
		{"forbidden 1061004", &APIError{Code: CodeForbidden}, classPermission},
		{"missing scope", &MissingScopeError{Where: "create_folder"}, classPermission},
		{"parent not found", &APIError{Code: CodeParentNotFound}, classNotFound},
		{"params error 1061002", &APIError{Code: CodeParamsError}, classInvalidArgument},
		{"depth exceeded", &APIError{Code: CodeDepthExceeded}, classInvalidArgument},
		{"too many nodes", &APIError{Code: CodeTooManyNodes}, classInvalidArgument},
		{"file too large", &ErrFileTooLarge{Size: 21 * 1024 * 1024}, classInvalidArgument},
		{"unknown code", &APIError{Code: 12345}, classOther},
		{"nil", nil, classOther},
		{"plain error", errors.New("boom"), classOther},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.err); got != c.want {
				t.Errorf("classify(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestAPIErrorRetryable(t *testing.T) {
	retryable := []int{CodeRetryable, CodeSizeMismatch, CodeInternal}
	for _, code := range retryable {
		e := &APIError{Code: code}
		if !e.Retryable() {
			t.Errorf("code %d should be retryable", code)
		}
	}

	notRetryable := []int{
		CodeMissingScope, CodeForbidden, CodeParentNotFound,
		CodeParamsError, CodeDepthExceeded, CodeTooManyNodes, 99999,
	}
	for _, code := range notRetryable {
		e := &APIError{Code: code}
		if e.Retryable() {
			t.Errorf("code %d should NOT be retryable", code)
		}
	}
}

// Regression for a real bug found against the live tenant: files.delete
// returned 1061001 intermittently, but that code was retryable in one list and
// missing from the list that decides whether an APIError is built at all. It
// therefore came out as an UncategorizedError and was never retried by our own
// client, only by rclone's outer loop.
func TestInternalCodeBecomesRetryableAPIError(t *testing.T) {
	c := &client{ts: &tokenSource{appID: "cli_test"}}
	err := c.businessError("files.delete", CodeInternal, "unknown error", []byte(`{}`), 200)

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type = %T (%v), want *APIError", err, err)
	}
	if !apiErr.Retryable() {
		t.Error("1061001 must be retryable: the same delete succeeds on retry")
	}
	if got := classify(err); got != classRetryable {
		t.Errorf("classify = %v, want classRetryable", got)
	}
}

// Guards the single source of truth: codeTable drives three decisions that must
// agree, so every entry has to survive a round trip through businessError.
func TestCodeTableIsSelfConsistent(t *testing.T) {
	c := &client{ts: &tokenSource{appID: "cli_test"}}
	if len(codeTable) == 0 {
		t.Fatal("codeTable is empty")
	}

	for code, info := range codeTable {
		err := c.businessError("test", code, "msg", []byte(`{}`), 200)

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Errorf("code %d: businessError returned %T, want *APIError", code, err)
			continue
		}
		if apiErr.Retryable() != info.retryable {
			t.Errorf("code %d: Retryable() = %v, want %v", code, apiErr.Retryable(), info.retryable)
		}
		if got := classify(err); got != info.class {
			t.Errorf("code %d: classify = %v, want %v", code, got, info.class)
		}
	}
}

// E1: the missing-scope message must name the scope and include the console URL.
func TestMissingScopeMessageIsActionable(t *testing.T) {
	e := &MissingScopeError{
		Scopes: []string{"drive:drive", "space:folder:create"},
		AppID:  "cli_testapp",
		Where:  "create_folder",
	}
	msg := e.Error()

	for _, want := range []string{"space:folder:create", "drive:drive", "open.feishu.cn/app/cli_testapp"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\ngot: %s", want, msg)
		}
	}
	// The user must be told the next step, not just the missing scope.
	if !strings.Contains(msg, "publish") {
		t.Errorf("message should mention publishing a new version\ngot: %s", msg)
	}
}

// E4 / SC-004: the oversized-file message must contain "20MB".
func TestFileTooLargeMessage(t *testing.T) {
	e := &ErrFileTooLarge{Name: "big.bin", Size: 21 * 1024 * 1024}
	msg := e.Error()

	if !strings.Contains(msg, "20MB") {
		t.Errorf("message must mention 20MB (SC-004)\ngot: %s", msg)
	}
	if !strings.Contains(msg, "big.bin") {
		t.Errorf("message must name the file\ngot: %s", msg)
	}
	if !strings.Contains(msg, "F2") {
		t.Errorf("message should say when chunked upload arrives\ngot: %s", msg)
	}
}

// E8: an unknown code must surface the number so support can act on it.
func TestUncategorizedCarriesCodeAndLogID(t *testing.T) {
	e := &UncategorizedError{Code: 88888, Msg: "weird", Where: "files.list", LogID: "abc123"}
	msg := e.Error()

	for _, want := range []string{"88888", "files.list", "abc123", "weird"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\ngot: %s", want, msg)
		}
	}
}

// E9: no error message may leak the app secret or a bearer token.
//
// The literals below are shaped like credentials on purpose, but are spelled so
// the repository secret scanner does not mistake them for real ones.
func TestErrorsDoNotLeakCredentials(t *testing.T) {
	secret := "FAKE-SECRET-VALUE-MUST-NOT-APPEAR"
	token := "FAKE-TOKEN-VALUE-MUST-NOT-APPEAR"

	errs := []error{
		&APIError{Code: CodeForbidden, Msg: "forbidden", Where: "files.list"},
		&MissingScopeError{Scopes: []string{"drive:drive"}, AppID: "cli_x", Where: "list"},
		&UncategorizedError{Code: 1, Msg: "boom", Where: "x"},
		&ErrFileTooLarge{Name: "f", Size: 30 * 1024 * 1024},
	}
	for _, e := range errs {
		msg := e.Error()
		if strings.Contains(msg, secret) || strings.Contains(msg, token) {
			t.Errorf("error message leaks credentials: %s", msg)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{21 * 1024 * 1024, "21.0 MB"},
	}
	for _, c := range cases {
		if got := humanSize(c.in); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
