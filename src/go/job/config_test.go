package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func mustLoad(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job file: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func loadErr(t *testing.T, body string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job file: %v", err)
	}
	_, err := Load(path)
	return err
}

// The defaults must be the safe ones: never delete unless asked, and a delete
// budget of 50.
func TestConfigDefaults(t *testing.T) {
	cfg := mustLoad(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/local"
[pair.remote]
backend = "feishu"
root = "team/docs"
`)
	p := &cfg.Pair[0]

	variant, err := p.EffectiveVariant(cfg)
	if err != nil {
		t.Fatalf("EffectiveVariant: %v", err)
	}
	if variant != VariantUpdate {
		t.Errorf("default variant = %q, want %q (a tool must not delete by default)",
			variant, VariantUpdate)
	}

	spec, err := p.EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec.Deletion != DeletionPermanent {
		t.Errorf("default deletion = %q", spec.Deletion)
	}
	if spec.MaxDelete != DefaultMaxDelete {
		t.Errorf("default max_delete = %d, want %d", spec.MaxDelete, DefaultMaxDelete)
	}
	if spec.VersioningDir != DefaultVersioningDir {
		t.Errorf("default versioning_dir = %q", spec.VersioningDir)
	}

	remote, err := p.RemotePath()
	if err != nil {
		t.Fatalf("RemotePath: %v", err)
	}
	if remote != "feishu:team/docs" {
		t.Errorf("RemotePath = %q, want feishu:team/docs", remote)
	}
}

// Job-level settings act as defaults; a pair can override them.
func TestConfigOverrides(t *testing.T) {
	cfg := mustLoad(t, `
[job]
name = "nightly"
variant = "mirror"
[job.sync]
max_delete = 100
deletion = "versioning"
versioning_naming = "timestamp-file"

[[pair]]
name = "work"
[pair.local]
root = "/tmp/a"
[pair.remote]
backend = "feishu"
root = "a"

[[pair]]
name = "photos"
[pair.local]
root = "/tmp/b"
[pair.remote]
backend = "feishu"
root = "b"
[pair.sync]
max_delete = 5
`)

	if v, _ := cfg.Pair[0].EffectiveVariant(cfg); v != VariantMirror {
		t.Errorf("pair 0 variant = %q, want mirror (inherited)", v)
	}
	s0, err := cfg.Pair[0].EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("pair 0: %v", err)
	}
	if s0.MaxDelete != 100 {
		t.Errorf("pair 0 max_delete = %d, want 100", s0.MaxDelete)
	}
	if s0.Deletion != DeletionVersioning || s0.VersioningNaming != NamingTimestampFile {
		t.Errorf("pair 0 sync = %+v", s0)
	}

	s1, err := cfg.Pair[1].EffectiveSync(cfg)
	if err != nil {
		t.Fatalf("pair 1: %v", err)
	}
	if s1.MaxDelete != 5 {
		t.Errorf("pair 1 max_delete = %d, want the override 5", s1.MaxDelete)
	}
}

// A mistyped variant must be an error. Falling back to update would silently
// stop syncing deletions; falling back to mirror would start deleting.
func TestConfigRejectsUnknownVariant(t *testing.T) {
	err := loadErr(t, `
[job]
variant = "mirorr"
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
`)
	if err == nil {
		t.Fatal("a mistyped variant must be rejected")
	}
	if !strings.Contains(err.Error(), "mirorr") {
		t.Errorf("error should quote the bad value\ngot: %v", err)
	}
}

func TestConfigRejectsUnimplementedVariants(t *testing.T) {
	for _, v := range []string{VariantTwoWay, VariantCustom} {
		err := loadErr(t, `
[job]
variant = "`+v+`"
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
`)
		if err == nil {
			t.Errorf("variant %q should be rejected as not implemented", v)
		} else if !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("variant %q: error should say it is not implemented\ngot: %v", v, err)
		}
	}
}

func TestConfigRejectsUnknownKeys(t *testing.T) {
	err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
max_del = 5
`)
	if err == nil {
		t.Fatal("a typo in a safety setting must not be silently ignored")
	}
}

func TestConfigRejectsBadDeletion(t *testing.T) {
	err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
deletion = "recycle-bin"
`)
	if err == nil {
		t.Fatal("unknown deletion should be rejected")
	}
	if !strings.Contains(err.Error(), "recycle-bin") {
		t.Errorf("error should quote the value\ngot: %v", err)
	}
}

// max_delete = 0 is a legitimate way to say "never delete"; a negative value is
// not, because it would look like a disabled guard.
func TestConfigMaxDeleteZeroAllowedNegativeRejected(t *testing.T) {
	if err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
max_delete = 0
`); err != nil {
		t.Fatalf("max_delete = 0 should be allowed: %v", err)
	}

	err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
max_delete = -1
`)
	if err == nil {
		t.Fatal("negative max_delete should be rejected")
	}
}

// max_delete_size is a size string, so the failure mode is a typo that would
// otherwise reach the guard as zero ("no limit") and silently disarm it.
func TestConfigMaxDeleteSize(t *testing.T) {
	c := mustLoad(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
max_delete_size = "1G"
`)
	spec, err := c.Pair[0].EffectiveSync(c)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec.MaxDeleteSize != 1<<30 {
		t.Errorf("MaxDeleteSize = %d, want %d", spec.MaxDeleteSize, 1<<30)
	}
	if spec.MaxDeleteSizeText != "1G" {
		t.Errorf("MaxDeleteSizeText = %q, want the text the user wrote", spec.MaxDeleteSizeText)
	}

	// Unset means no ceiling, not "refuse everything".
	c2 := mustLoad(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
`)
	spec2, err := c2.Pair[0].EffectiveSync(c2)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec2.MaxDeleteSize != 0 {
		t.Errorf("unset MaxDeleteSize = %d, want 0", spec2.MaxDeleteSize)
	}

	if err := loadErr(t, `
[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
[pair.sync]
max_delete_size = "1GB of stuff"
`); err == nil {
		t.Fatal("a non-size max_delete_size should be rejected at load, not at the guard")
	}

	// The job-level value is a default the pair inherits.
	c3 := mustLoad(t, `
[job]
name = "j"
[job.sync]
max_delete_size = "500M"

[[pair]]
name = "p"
[pair.local]
root = "/tmp/l"
[pair.remote]
backend = "feishu"
root = "r"
`)
	spec3, err := c3.Pair[0].EffectiveSync(c3)
	if err != nil {
		t.Fatalf("EffectiveSync: %v", err)
	}
	if spec3.MaxDeleteSize != 500<<20 {
		t.Errorf("inherited MaxDeleteSize = %d, want %d", spec3.MaxDeleteSize, 500<<20)
	}
}

func TestConfigRemotePath(t *testing.T) {
	cases := []struct {
		name    string
		pair    Pair
		want    string
		wantErr bool
	}{
		{
			name: "backend plus root",
			pair: Pair{Name: "p", Remote: Endpoint{Backend: "feishu", Root: "a/b"}},
			want: "feishu:a/b",
		},
		{
			name: "full path without backend",
			pair: Pair{Name: "p", Remote: Endpoint{Root: "feishu:a/b"}},
			want: "feishu:a/b",
		},
		{
			name:    "bare root without backend is ambiguous",
			pair:    Pair{Name: "p", Remote: Endpoint{Root: "a/b"}},
			wantErr: true,
		},
		{
			name:    "backend must not be a path",
			pair:    Pair{Name: "p", Remote: Endpoint{Backend: "feishu:x", Root: "a"}},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.pair.RemotePath()
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RemotePath: %v", err)
			}
			if got != c.want {
				t.Errorf("RemotePath = %q, want %q", got, c.want)
			}
		})
	}
}

func TestConfigRejectsDuplicatePairNames(t *testing.T) {
	err := loadErr(t, `
[[pair]]
name = "dup"
[pair.local]
root = "/tmp/a"
[pair.remote]
backend = "feishu"
root = "a"

[[pair]]
name = "dup"
[pair.local]
root = "/tmp/b"
[pair.remote]
backend = "feishu"
root = "b"
`)
	if err == nil {
		t.Fatal("duplicate pair names should be rejected")
	}
}
