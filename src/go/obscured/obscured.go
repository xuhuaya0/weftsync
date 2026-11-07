// Package obscured decides how a stored credential value should be read.
//
// rclone marks password options with IsPassword, and its own tooling stores those
// values obscured: base64url(IV[16] || AES-CTR(plaintext)). A backend must reveal
// them before use. The awkward part, and the reason this is a shared package rather
// than a few lines in one backend, is that the two forms cannot be told apart by
// inspection:
//
//   - Reveal only validates the base64 and the 16-byte minimum.
//   - A 32-character alphanumeric secret *is* valid base64url and long enough.
//   - So Reveal on a hand-written plaintext value succeeds and returns noise.
//     Measured: a 32-character secret reveals to 8 bytes of garbage, no error.
//
// Nothing here guesses with certainty, therefore. Forms reports which reading to
// try first and how the caller should describe it; the authority on whether a
// credential is right is the provider's authentication call, and callers must be
// prepared to retry with the other reading.
package obscured

import (
	"github.com/rclone/rclone/fs/config/obscure"
)

// Forms reports how to read a stored value.
//
// primary is the value to try first. encoded is true when that value came from
// revealing an obscured blob, which means the stored text is not the secret itself.
// The stored text is returned as the second candidate when the two readings differ,
// so a caller can retry with it.
//
// An empty input yields ("", false).
func Forms(stored string) (primary, fallback string, encoded bool) {
	if stored == "" {
		return "", "", false
	}
	revealed, err := obscure.Reveal(stored)
	if err != nil || revealed == "" || revealed == stored || !printable(revealed) {
		// Either it is not obscured at all, or revealing produced bytes no
		// credential would contain. Use it as written.
		return stored, "", false
	}
	return revealed, stored, true
}

// printable reports whether every byte is printable ASCII.
//
// A real secret is printable; the noise produced by revealing plaintext usually is
// not, so this keeps the common plaintext case from making a doomed request.
func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return s != ""
}

// Encode obscures a value for storage.
//
// Used when weftsync writes a credential itself, so the file does not hold the
// secret in readable form. rclone's own `config create` decides this for itself,
// and its check is the same unreliable one described above: it leaves a 32-character
// secret in plaintext because Reveal appears to succeed. Writing the obscured form
// unconditionally is the only way to be sure of what lands in the file - which is
// safe because readers handle both forms.
func Encode(value string) (string, error) {
	return obscure.Obscure(value)
}
