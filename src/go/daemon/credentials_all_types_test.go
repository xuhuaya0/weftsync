package daemon

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"

	// Link every backend rclone ships, the same set main.go links. Without this the
	// daemon's tests only see whatever backends another test file happened to
	// import, and a test that claims to check "all types" would check one of them.
	// The guard below fails loudly if this import is ever removed.
	_ "github.com/rclone/rclone/backend/all"
)

// TestEveryBackendRendersSaneFields checks the credential panel's data for every
// backend type, not just the one that happened to be looked at.
//
// This exists because a layout defect was found on the SMB card after only the
// feishu card had been eyeballed: "I can't check every one by hand, don't you have
// a check for all of them?" The visual column alignment is checked separately (see
// scripts/check_field_layout.js, which measures the rendered DOM); what can be
// checked here is everything about the field list itself, for every type at once,
// in milliseconds, on every `go test`.
func TestEveryBackendRendersSaneFields(t *testing.T) {
	withTempConfig(t, "")
	a := credentialAPI(t)

	types := a.remoteTypes()
	// The guard. A single linked backend would make every assertion below pass
	// trivially while proving nothing.
	if len(types) < 40 {
		t.Fatalf("only %d types visible; the all-backends import is missing and this "+
			"test would prove nothing", len(types))
	}

	checked := 0
	for _, rt := range types {
		rt := rt
		t.Run(rt.Name, func(t *testing.T) {
			// A pending type has no card to render, so its field list does not
			// matter here; skip it rather than assert on something unreachable.
			if rt.Pending {
				t.Skip("pending type: no card is rendered for it")
			}
			checked++

			v := a.credentialView(rt.Name, rt.Name, nil)
			if v.Name != rt.Name {
				t.Fatalf("view name = %q", v.Name)
			}
			if v.Detail != "" {
				t.Errorf("backend reports a problem: %s", v.Detail)
			}
			if !v.Testable {
				// Every type the picker offers can be opened; a "not testable" card
				// would show a permanently disabled 测试连接 button.
				t.Errorf("not testable")
			}

			seen := map[string]bool{}
			seenOptional := false
			for _, f := range v.Fields {
				if f.Name == "" {
					t.Error("a field has no name")
					continue
				}
				if seen[f.Name] {
					t.Errorf("duplicate field %q", f.Name)
				}
				seen[f.Name] = true

				if f.Label == "" {
					t.Errorf("field %q has no label; the row would render blank", f.Name)
				}
				// Required fields must come first: the form renders them in the
				// order received, and a required field below three optional ones is
				// a form nobody finishes.
				if !f.Required {
					seenOptional = true
				} else if seenOptional {
					t.Errorf("required field %q is listed after an optional one", f.Name)
				}

				if f.Secret {
					if f.Value != "" {
						t.Errorf("secret field %q carries a value", f.Name)
					}
					if f.Set && f.Length == 0 && !f.FromEnv {
						t.Errorf("secret field %q is set but reports no length", f.Name)
					}
				}
			}
		})
	}

	if checked < 40 {
		t.Fatalf("only %d types were checked", checked)
	}
	t.Logf("checked %d types", checked)
}

// TestRenderedFieldsMatchTheBackend guards against the panel inventing or dropping
// a field, and against the same field being rendered many times over.
//
// It deliberately does NOT re-derive the panel's option list and compare it for
// equality: that would be the implementation checked against itself, and it would
// pass whatever the filter did. Instead it asserts properties that come straight
// from rclone's declarations:
//
//  1. no field name appears twice            (the s3 bug: 23 rows called "endpoint")
//  2. every rendered name exists on the backend  (no invented fields)
//  3. every REQUIRED option that applies to the selected provider is rendered
//     (no connection that cannot work)
//
// "Applies to the selected provider" is spelled out here in the test's own terms
// (unscoped, or scoped to the provider that will actually be used), so a change to
// the panel's filter that starts dropping required options fails this test.
func TestRenderedFieldsMatchTheBackend(t *testing.T) {
	withTempConfig(t, "")
	a := credentialAPI(t)

	types := a.remoteTypes()
	if len(types) < 40 {
		t.Skip("all-backends import missing")
	}

	for _, rt := range types {
		if rt.Pending {
			continue
		}
		reg, err := fs.Find(rt.Name)
		if err != nil {
			t.Fatalf("%s: %v", rt.Name, err)
		}

		known := map[string]bool{}
		for _, o := range reg.Options {
			known[o.Name] = true
		}

		v := a.credentialView(rt.Name, rt.Name, nil)

		got := map[string]int{}
		for _, f := range v.Fields {
			got[f.Name]++
		}

		// 1. no duplicates, and 2. nothing invented.
		var dupes, invented []string
		for n, c := range got {
			if c > 1 {
				dupes = append(dupes, fmt.Sprintf("%s×%d", n, c))
			}
			if !known[n] {
				invented = append(invented, n)
			}
		}
		sort.Strings(dupes)
		sort.Strings(invented)
		if len(dupes) > 0 {
			t.Errorf("%s: duplicate field names %v (provider-scoped options not filtered?)",
				rt.Name, dupes)
		}
		if len(invented) > 0 {
			t.Errorf("%s: fields the backend does not declare: %v", rt.Name, invented)
		}

		// 3. every required option that applies to the selected provider.
		var missing []string
		for _, o := range reg.Options {
			if !o.Required {
				continue
			}
			if o.Provider != "" && v.Provider != "" &&
				!strings.EqualFold(o.Provider, v.Provider) {
				continue // belongs to a different provider; correctly absent
			}
			if got[o.Name] == 0 {
				missing = append(missing, o.Name)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: required options not rendered: %v", rt.Name, missing)
		}
	}
}

// TestProviderFilterMatchesRcloneContract states the provider rule independently
// of the implementation and compares the two for every provider rclone declares.
//
// Why a second implementation instead of just calling fs.MatchProvider: the point
// is to pin the CONTRACT, not to re-call the same function. The defect this guards
// against is a comparison that looks right and is not - an earlier version used
// strings.EqualFold against the whole `Provider` string, which silently dropped
// every negated declaration ("!AWS,ArvanCloud,..."), and Minio ended up with no
// `endpoint` field at all. A reference filter written from rclone's documented
// behaviour catches that; calling the same helper twice would not.
func TestProviderFilterMatchesRcloneContract(t *testing.T) {
	ref := func(cfg, prov string) bool {
		// rclone: blank config or blank provider matches everything; a leading "!"
		// negates the comma-separated list.
		if cfg == "" || prov == "" {
			return true
		}
		neg := strings.HasPrefix(cfg, "!")
		if neg {
			cfg = cfg[1:]
		}
		hit := false
		for _, p := range strings.Split(cfg, ",") {
			if p == prov {
				hit = true
				break
			}
		}
		if neg {
			return !hit
		}
		return hit
	}

	checked := 0
	for _, ri := range fs.Registry {
		if ri == nil || ri.Name == "" || ri.Hide {
			continue
		}
		popt := ri.Options.Get("provider")
		if popt == nil || len(popt.Examples) == 0 {
			continue
		}
		provs := []string{""} // the "nothing stored" case
		for _, ex := range popt.Examples {
			provs = append(provs, ex.Value)
		}

		for _, prov := range provs {
			var want []string
			seen := map[string]bool{}
			for _, o := range ri.Options {
				if o.Advanced && !o.Required {
					continue
				}
				if o.Hide&fs.OptionHideConfigurator != 0 && !o.Required {
					continue
				}
				if !ref(o.Provider, prov) || seen[o.Name] {
					continue
				}
				seen[o.Name] = true
				want = append(want, o.Name)
			}

			var got []string
			for _, o := range applicableOptions(ri, prov) {
				got = append(got, o.Name)
			}

			sort.Strings(want)
			sort.Strings(got)
			if strings.Join(want, ",") != strings.Join(got, ",") {
				missing, extra := diffNames(want, got)
				t.Errorf("%s provider=%q: missing %v, unexpected %v",
					ri.Name, prov, missing, extra)
			}
			checked++
		}
	}
	if checked < 40 {
		t.Errorf("only %d provider/backend combinations checked", checked)
	}
	t.Logf("compared %d provider/backend combinations against the contract", checked)
}

// diffNames reports what is in want but not got, and vice versa.
func diffNames(want, got []string) (missing, extra []string) {
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
	for _, s := range want {
		if !g[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range got {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	return missing, extra
}

// A field the backend limits to a list must be offered as a list. Rendering s3's
// `provider` as a text box would require typing "IBMCOS" exactly, and a typo
// surfaces much later as an authentication error.
func TestChoiceFieldsCarryTheirChoices(t *testing.T) {
	withTempConfig(t, "")
	a := credentialAPI(t)

	reg, err := fs.Find("s3")
	if err != nil {
		t.Skip("s3 backend not linked")
	}
	popt := reg.Options.Get("provider")
	if popt == nil || len(popt.Examples) < 2 {
		t.Skip("s3 declares no provider list in this build")
	}

	v := a.credentialView("choicearc", "s3", nil)
	var provider, storage *CredentialField
	for i := range v.Fields {
		switch v.Fields[i].Name {
		case "provider":
			provider = &v.Fields[i]
		case "storage_class":
			storage = &v.Fields[i]
		}
	}
	if provider == nil {
		t.Fatal("provider field missing")
	}
	if len(provider.Choices) < 2 {
		t.Errorf("provider has %d choices, expected rclone's full list", len(provider.Choices))
	}
	// The value shown must be the one that will actually be used, even when
	// nothing is stored - otherwise the form says "no provider" while rendering
	// AWS's fields.
	if provider.Value == "" {
		t.Errorf("provider value is empty; it should report the effective provider %q", v.Provider)
	}
	if provider.Value != v.Provider {
		t.Errorf("provider shows %q but %q is in force", provider.Value, v.Provider)
	}

	// storage_class is provider-scoped too: Alibaba must not offer AWS's classes.
	if storage != nil && len(storage.Choices) > 0 {
		config.FileSetValue("choicearc", "provider", "Alibaba")
		defer config.FileDeleteKey("choicearc", "provider")
		for _, f := range a.credentialView("choicearc", "s3", nil).Fields {
			if f.Name != "storage_class" {
				continue
			}
			if len(f.Choices) == 0 {
				t.Error("storage_class lost its choices under Alibaba")
			}
			if len(f.Choices) >= len(storage.Choices) {
				t.Errorf("storage_class choices did not narrow for Alibaba: %d vs %d",
					len(f.Choices), len(storage.Choices))
			}
		}
	}
}
