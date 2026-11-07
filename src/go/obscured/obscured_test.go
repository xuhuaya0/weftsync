package obscured

import (
	"strings"
	"testing"
)

// A value written by rclone's tooling must be usable.
func TestFormsRevealsObscured(t *testing.T) {
	secret := strings.Repeat("ab12CD34", 4) // 32 alphanumeric chars: a real secret's shape
	enc, err := Encode(secret)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if enc == secret {
		t.Fatal("Encode returned its input, so this test proves nothing")
	}

	primary, fallback, encoded := Forms(enc)
	if !encoded {
		t.Error("an obscured value must be reported as encoded")
	}
	if primary != secret {
		t.Errorf("primary has %d bytes, want the %d-byte secret", len(primary), len(secret))
	}
	if fallback != enc {
		t.Error("the as-stored form should be offered as a fallback")
	}
}

// A hand-written plaintext secret must be used as written, with no fallback.
//
// This is the case where Reveal succeeds on plaintext and returns noise, so the
// printable check is what keeps the two apart.
func TestFormsKeepsPlaintext(t *testing.T) {
	secret := strings.Repeat("ab12CD34", 4)

	primary, fallback, encoded := Forms(secret)
	if encoded {
		t.Error("plaintext must not be reported as encoded")
	}
	if primary != secret {
		t.Errorf("primary = %d bytes, want the value unchanged", len(primary))
	}
	if fallback != "" {
		t.Errorf("fallback = %q, want none", fallback)
	}
}

// Short or unusual values must still be used as written.
func TestFormsAcceptsOddPlaintext(t *testing.T) {
	for _, s := range []string{"x", "short", "has spaces and symbols!@#$%^&*()"} {
		primary, _, encoded := Forms(s)
		if encoded {
			t.Errorf("%q was reported as encoded", s)
		}
		if primary != s {
			t.Errorf("%q became %q", s, primary)
		}
	}
}

func TestFormsEmpty(t *testing.T) {
	primary, fallback, encoded := Forms("")
	if primary != "" || fallback != "" || encoded {
		t.Errorf(`Forms("") = (%q, %q, %v), want all zero`, primary, fallback, encoded)
	}
}

// Encode must round-trip through Forms, otherwise we would write values we cannot
// read back.
func TestEncodeRoundTrip(t *testing.T) {
	for _, secret := range []string{"a", strings.Repeat("ab12CD34", 4), "with-dash_under"} {
		enc, err := Encode(secret)
		if err != nil {
			t.Fatalf("Encode(%q): %v", secret, err)
		}
		primary, _, _ := Forms(enc)
		if primary != secret {
			t.Errorf("round trip of %q gave %q", secret, primary)
		}
	}
}

// Encode must never return the value unchanged: a caller relies on that to know the
// file no longer holds the secret.
func TestEncodeChangesTheValue(t *testing.T) {
	for _, secret := range []string{"x", strings.Repeat("ab12CD34", 4)} {
		enc, err := Encode(secret)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if enc == secret {
			t.Errorf("%q was not obscured", secret)
		}
	}
}
