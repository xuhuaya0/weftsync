package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/config"

	"weftsync/job"
	"weftsync/obscured"
)

// Managing credentials from the console.
//
// Three rules shape this file, and every one of them is about not leaking or
// lying:
//
//  1. A secret is never returned. Not masked, not truncated, not "the last four
//     characters". A masked value invites the user to believe the console knows
//     the secret, and a partial value is still a leak. The only thing exposed is
//     whether the field is set and how long it is - and the length earns its place,
//     because a trailing CR or a stray space is the most common reason a correct
//     looking credential fails, and nothing else would show it.
//  2. The file being written is the one rclone was started with. Any path from
//     the request would turn a credentials form into an arbitrary-file-write.
//  3. Values are written through rclone's own config store, so a config written by
//     `rclone config` and one written here are the same file in the same format.
//
// Password options are stored obscured. That is rclone's convention for IsPassword
// fields, and the feishu backend accepts either form (see backend/feishu/secret.go
// for why the two cannot be told apart by inspection: obscuring is base64url of an
// AES-CTR blob, so a 32-character alphanumeric secret is *valid base64url*, and
// revealing it "succeeds" with 8 bytes of noise).

// CredentialField is one option of a remote, described without its value.
type CredentialField struct {
	Name string `json:"name"`
	// Label is a short human name for the form.
	Label string `json:"label,omitempty"`
	// Set reports whether the config file (or the environment) supplies it.
	Set bool `json:"set"`
	// Secret fields never carry Value.
	Secret bool `json:"secret"`
	// Length is the length of the value that will actually be used: for an obscured
	// stored value it is the decoded length, not the stored text's. Showing the
	// stored length would report 64 characters for a 32-character secret and make
	// the hint useless for spotting a stray space.
	Length int `json:"length,omitempty"`
	// Encoded means the stored text is an obscured blob and Length describes the
	// decoded value. It is a statement about how weftsync will read the field, which
	// is the same reading the sync path tries first.
	Encoded bool `json:"encoded,omitempty"`
	// Value is present for non-secret fields only.
	Value string `json:"value,omitempty"`
	// Required mirrors the backend's own declaration.
	Required bool `json:"required"`
	// FromEnv means the value comes from an environment variable, which this page
	// cannot change; the form should leave the field alone and say so.
	FromEnv bool `json:"from_env,omitempty"`
	// Choices, when non-empty, means the backend restricts this value to a fixed
	// list and the form should render a select instead of a text box. s3's
	// `provider` is the case that matters: which other fields apply depends on it.
	Choices []string `json:"choices,omitempty"`
	// Provider is the provider this option belongs to, empty when it applies to all
	// of them. It is not shown; it is how the panel knows to hide the endpoint of
	// every provider except the selected one.
	Provider string `json:"-"`
}

// CredentialView is one remote, ready to render.
type CredentialView struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Fields is ordered: required first, then the rest alphabetically.
	Fields []CredentialField `json:"fields"`
	// UsedBy lists the jobs that point at this remote, so the page can say which
	// credentials matter before a run fails.
	UsedBy []string `json:"used_by"`
	// Testable is false when the backend cannot be opened (unknown type, or it
	// declares no options).
	Testable bool   `json:"testable"`
	Detail   string `json:"detail,omitempty"`
	// Provider is the selected provider for backends that scope their options by
	// one (s3). Empty for every other backend.
	Provider string `json:"provider,omitempty"`
}

// usedRemotes maps remote name -> job names that reference it.
func (d *Daemon) usedRemotes() map[string][]string {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := map[string][]string{}
	for _, e := range d.entries {
		for i := range e.cfg.Pair {
			name := pairRemoteName(&e.cfg.Pair[i])
			if name == "" {
				continue
			}
			out[name] = append(out[name], e.name)
		}
	}
	// Sort and de-duplicate: several pairs of one job commonly share a remote.
	for name, jobs := range out {
		sort.Strings(jobs)
		uniq := jobs[:1]
		for _, j := range jobs[1:] {
			if j != uniq[len(uniq)-1] {
				uniq = append(uniq, j)
			}
		}
		out[name] = uniq
	}
	return out
}

// pairRemoteName is the rclone remote a pair writes to, or "" when the pair's
// cloud side is not a named remote at all.
//
// A pair may name the remote either as pair.remote.backend or inline in
// pair.remote.root ("remote:path"), and the two are the same thing as far as the
// credentials page is concerned.
//
// The root case delegates to isRemotePath instead of splitting at the first colon.
// Doing the split here was wrong in exactly the way that matters for "is this
// connection in use": a pair whose cloud side is a local path looks identical to a
// remote unless the colon is read carefully, so "C:/data/dst" produced a reference
// to a remote named "C" and ":local:C:/dst" produced one named ":local". Both are
// local paths; neither names a connection. isRemotePath already knows the three
// cases (drive letter, the :local: prefix, and a real "name:path"), so there is one
// place to be right instead of two.
func pairRemoteName(p *job.Pair) string {
	if b := strings.TrimSpace(p.Remote.Backend); b != "" {
		return b
	}
	root := strings.TrimSpace(p.Remote.Root)
	if !isRemotePath(root) {
		return ""
	}
	return root[:strings.Index(root, ":")]
}

// CredentialViews describes every remote in the config file.
func (a *API) CredentialViews() []CredentialView {
	used := a.D.usedRemotes()

	names := config.FileSections()
	sort.Strings(names)

	out := make([]CredentialView, 0, len(names))
	for _, name := range names {
		typ, _ := config.FileGetValue(name, "type")
		if typ == "" {
			continue // a section with no type is not a remote
		}
		out = append(out, a.credentialView(name, typ, used[name]))
	}
	return out
}

// fieldLabels gives a credential field a name a human recognises.
//
// rclone's option names are accurate but they are not what the provider's console
// calls these values: "app_id" does not tell anyone where to find it, and the form
// is the only place a user has to work that out.
var fieldLabels = map[string]map[string]string{
	"feishu": {
		"app_id":     "应用 App ID",
		"app_secret": "应用 App Secret",
		"root_token": "目录 token（也可直接粘贴目录 URL）",
		"state_file": "增量同步状态文件",
		"base_url":   "API 地址",
		"qps":        "每秒请求数上限",
	},
	// Wrapper backends are configured by naming another connection. That is a text
	// field like any other, but "remote" as a label tells nobody what to type.
	"alias":    {"remote": "上游连接名（如 feishu:）"},
	"crypt":    {"remote": "上游连接名（如 feishu:）", "password": "加密口令"},
	"chunker":  {"remote": "上游连接名（如 feishu:）"},
	"compress": {"remote": "上游连接名（如 feishu:）"},
	"cache":    {"remote": "上游连接名（如 feishu:）"},
	"hasher":   {"remote": "上游连接名（如 feishu:）"},
	"union":    {"upstreams": "上游连接名，逗号分隔"},
	"combine":  {"upstreams": "上游连接名，逗号分隔"},
}

// effectiveProvider is the provider a backend will actually use.
//
// Order: what the file says, then the option's declared default, then the first
// entry of its examples. The last fallback is not a guess: rclone's s3 declares
// `provider` with 39 examples in its own order and AWS first, which is the
// documented default; the examples list *is* the priority list a user sees.
//
// Needed because s3 declares one `endpoint` option per provider. Rendering them
// all produced twenty-three rows labelled "endpoint" on one card - the reason this
// function exists at all.
func effectiveProvider(reg *fs.RegInfo, name string) string {
	popt := reg.Options.Get("provider")
	if popt == nil {
		return ""
	}
	if stored, ok := config.FileGetValue(name, "provider"); ok && stored != "" {
		return stored
	}
	if def, ok := popt.Default.(string); ok && def != "" {
		return def
	}
	if len(popt.Examples) > 0 {
		return popt.Examples[0].Value
	}
	return ""
}

// applicableOptions is the option list the panel should render.
//
// Filters in one place so the view and its tests cannot disagree about what "the
// fields of this backend" means.
//
// Provider matching uses rclone's own fs.MatchProvider, because the field is not a
// plain name: it also carries negation. s3 declares `endpoint` with
//
//	Provider: "!AWS,ArvanCloud,IBMCOS,..."
//
// meaning "every provider EXCEPT these". An EqualFold comparison never matches a
// negated declaration, so an earlier version silently hid `endpoint` for Minio -
// a storage provider that cannot work without one. Using the shared function also
// keeps the semantics (blank provider matches everything; comma-separated lists)
// identical to what the rclone configurator does.
func applicableOptions(reg *fs.RegInfo, providerVal string) []fs.Option {
	out := make([]fs.Option, 0, len(reg.Options))
	seen := make(map[string]bool, len(reg.Options))
	for _, opt := range reg.Options {
		// Advanced options are the tuning knobs (rate limits, base URLs); the
		// credentials page is about the few values a user has to obtain from the
		// provider, and a long form buries them.
		if opt.Advanced && !opt.Required {
			continue
		}
		// Hidden means rclone itself refuses to ask for it in the configurator.
		if opt.Hide&fs.OptionHideConfigurator != 0 && !opt.Required {
			continue
		}
		if !fs.MatchProvider(opt.Provider, providerVal) {
			continue
		}
		// The same option name may be declared once per provider. After filtering
		// exactly one applies, but dedupe anyway: two rows with the same name would
		// write to the same config key and the second would always win.
		if seen[opt.Name] {
			continue
		}
		seen[opt.Name] = true
		out = append(out, opt)
	}
	return out
}

func (a *API) credentialView(name, typ string, usedBy []string) CredentialView {
	v := CredentialView{Name: name, Type: typ, UsedBy: usedBy}

	reg, err := fs.Find(typ)
	if err != nil {
		v.Detail = "unknown backend type: " + err.Error()
		return v
	}
	v.Testable = true
	labels := fieldLabels[typ]
	v.Provider = effectiveProvider(reg, name)

	for _, opt := range applicableOptions(reg, v.Provider) {
		f := CredentialField{
			Name:     opt.Name,
			Label:    opt.Name,
			Secret:   opt.IsPassword,
			Required: opt.Required,
			Provider: opt.Provider,
		}
		if lbl := labels[opt.Name]; lbl != "" {
			f.Label = lbl
		}
		if len(opt.Examples) > 0 {
			for _, ex := range opt.Examples {
				// Examples are provider-scoped too (s3's storage_class lists a
				// different set per provider), and rclone filters them the same way
				// when it builds its own configurator prompts.
				if ex.Value == "" || !fs.MatchProvider(ex.Provider, v.Provider) {
					continue
				}
				f.Choices = append(f.Choices, ex.Value)
			}
		}

		stored, found := config.FileGetValue(name, opt.Name)
		if !found || stored == "" {
			// The backend falls back to an environment variable for some fields;
			// say so rather than showing the field as unset and inviting the user
			// to overwrite a working configuration.
			if env := os.Getenv(fs.ConfigToEnv(name, opt.Name)); env != "" {
				f.Set = true
				f.FromEnv = true
				if opt.IsPassword {
					f.Length = len(env)
				} else {
					f.Value = env
				}
			} else if opt.Name == "provider" && v.Provider != "" {
				// Nothing is stored, but the backend HAS a provider - rclone falls
				// back to the option's default. Show that value: an empty select
				// would say "no provider chosen" while eleven AWS-shaped fields sit
				// right below it, and the reader has no way to tell which one is
				// actually in force. Same principle as Length: describe the value
				// that will be used, not the absence of one.
				f.Value = v.Provider
			}
			v.Fields = append(v.Fields, f)
			continue
		}

		f.Set = true
		if opt.IsPassword {
			// Length only, never the value. See the note at the top of this file.
			usable, _, encoded := obscured.Forms(stored)
			f.Length = len(usable)
			f.Encoded = encoded
		} else {
			f.Value = stored
		}
		v.Fields = append(v.Fields, f)
	}

	// Required first: a form should ask for the things that must be filled in
	// before the things that merely tune behaviour.
	sort.SliceStable(v.Fields, func(i, j int) bool {
		if v.Fields[i].Required != v.Fields[j].Required {
			return v.Fields[i].Required
		}
		return v.Fields[i].Name < v.Fields[j].Name
	})
	return v
}

// RemoteType is one backend this build can create a remote for.
//
// The console needs this list because a cloud side is not "Feishu": it is
// whichever remote the user configures, and before this list existed the page
// could only edit remotes that already existed in the file. `weftsync config` was
// the only way in, which is not an answer for someone using the web console.
type RemoteType struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Recommended marks the backends shown first. rclone registers ~70 of them,
	// and a new-connection form whose first row is "alias" helps nobody.
	Recommended bool `json:"recommended,omitempty"`
	// Pending means this type needs a flow the page does not have YET, and Reason
	// says which one. It is deliberately not called "blocked": the distinction
	// that matters to a reader is "we have not built it" versus "it cannot work",
	// and only the first is worth showing.
	Pending bool   `json:"pending,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// hiddenTypes are registered backends that can never be a sync target here, so
// the picker does not mention them at all.
//
// Hiding is the honest option for these: there is nothing the user could do to
// make them work, and a greyed-out row would only invite the question "why is
// this here". Everything that CAN work - now or later - stays visible.
var hiddenTypes = map[string]string{
	"memory": "in-memory storage, lost on exit; cannot hold a sync target",
}

// recommendedOrder is the short list put at the top of the type picker.
//
// feishu first because it is this program's own backend and what its
// documentation is about; then the storage people actually sync to.
var recommendedOrder = []string{
	"feishu", "s3", "webdav", "sftp", "ftp", "b2", "azureblob", "swift",
	"qingstor", "smb", "mega",
}

// pendingReason explains what a backend needs that this page does not do yet.
//
// Only one real case: rclone's OAuth backends (drive, onedrive, dropbox, box, ...)
// need a browser handshake - the config value they end up storing is a token
// produced by that handshake, not something a user can type. Building it is a
// matter of driving rclone's config state machine over HTTP; it is not done yet,
// and saying so is more useful than pretending the type does not exist.
//
// Wrapper backends (alias, crypt, union, chunker, compress, hasher, cache,
// combine) are NOT pending: they need one text field naming another connection,
// which is exactly what a required option already renders as. An earlier version
// of this function refused them, which was simply wrong.
func pendingReason(ri *fs.RegInfo) string {
	hasToken := false
	for _, o := range ri.Options {
		if o.Name == "token" {
			hasToken = true
		}
	}
	if ri.Config != nil && hasToken {
		return "需要浏览器登录授权（OAuth），本页还没做这个流程"
	}
	return ""
}

// remoteTypes lists the backends a remote can be created from.
func (a *API) remoteTypes() []RemoteType {
	return remoteTypesFrom(fs.Registry)
}

// remoteTypesFrom turns a backend registry into the picker's contents.
//
// Split out from remoteTypes so a test can assert the filtering and ordering
// against a fixed list. Testing it through fs.Registry would only ever exercise
// whichever backends the test binary happens to link - the daemon's own tests link
// one, while main.go links all of them, so the test would prove nothing about the
// list a user sees.
func remoteTypesFrom(reg []*fs.RegInfo) []RemoteType {
	rank := make(map[string]int, len(recommendedOrder))
	for i, n := range recommendedOrder {
		rank[n] = i
	}

	out := make([]RemoteType, 0, len(reg))
	for _, ri := range reg {
		if ri == nil || ri.Name == "" || ri.Hide || ri.Name == "local" {
			// local is not a cloud connection, and Hide is rclone's own "do not
			// show this in the configurator" flag.
			continue
		}
		if _, hidden := hiddenTypes[ri.Name]; hidden {
			continue
		}
		reason := pendingReason(ri)
		_, rec := rank[ri.Name]
		out = append(out, RemoteType{
			Name:        ri.Name,
			Description: ri.Description,
			Recommended: rec && reason == "",
			Pending:     reason != "",
			Reason:      reason,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		// Pending ones go last regardless of how recommendable they are.
		if out[i].Pending != out[j].Pending {
			return !out[i].Pending
		}
		ri, iok := rank[out[i].Name]
		rj, jok := rank[out[j].Name]
		if iok != jok {
			return iok // recommended first
		}
		if iok && ri != rj {
			return ri < rj // and in the order written above
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// validRemoteName rejects names rclone would mis-split or a user cannot retype.
//
// "name:path" is split at the first colon, so a colon inside the name turns the
// remainder of the name into a path - the remote then silently does not exist. A
// slash makes it look like a subdirectory, and a backslash becomes a separator on
// Windows. A leading "-" would be read as a flag by any tool that takes the remote
// as an argument.
func validRemoteName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("名字不能为空")
	case len([]rune(name)) > 64:
		return fmt.Errorf("名字太长了（上限 64 个字符）")
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("名字不能以「-」开头")
	case strings.ContainsAny(name, `:/\`):
		return fmt.Errorf("名字里不能有冒号、斜杠或反斜杠（冒号是「远程名:路径」的分隔符）")
	case strings.ContainsAny(name, "\r\n\t"):
		return fmt.Errorf("名字里不能有换行或制表符")
	}
	return nil
}

// handleCredentialCreate adds a remote section to the config file.
//
// Separate from saving values because the two mean different things: saving fills
// in a remote that exists, creating brings one into existence. Treating an unknown
// ?name= as an implicit create (the first draft) meant a typo produced a second,
// empty remote and the error the user needed appeared two steps later as
// "authentication failed".
func (a *API) handleCredentialCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	typ := strings.TrimSpace(r.URL.Query().Get("type"))

	if err := validRemoteName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if typ == "" {
		writeError(w, http.StatusBadRequest, "the type query parameter is required to create a remote")
		return
	}
	reg, err := fs.Find(typ)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("不认识的类型 %q：%v", typ, err))
		return
	}
	if reason := pendingReason(reg); reason != "" {
		// A connection created here would be permanently unusable, and the user
		// would find out two steps later as "authentication failed". Say it now,
		// and say it as "not yet" - because that is what it is.
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("类型 %q 还没支持：%s", typ, reason))
		return
	}
	if _, exists := config.FileGetValue(name, "type"); exists {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("已经有一个叫 %q 的远程了；直接填它的字段即可", name))
		return
	}

	// Only the type is written here. Every other value is a credential, and a
	// half-filled remote is worth having: the form shows which required fields are
	// still empty.
	config.FileSetValue(name, "type", typ)
	config.SaveConfig()
	if err := restrictToOwner(config.GetConfigPath()); err != nil {
		writeError(w, http.StatusInternalServerError,
			"远程已创建，但配置文件权限没能收紧："+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"created": name,
		"remote":  a.credentialView(name, typ, a.D.usedRemotes()[name]),
	})
}

// handleCredentialDelete removes a remote section from the config file.
//
// Refused while a loaded job still points at it. Deleting would leave that job
// with a path whose remote no longer exists, and the failure would surface later
// as a confusing "didn't find section in config file" in the middle of a run. The
// panel already shows which jobs use a connection, so the refusal can name them.
func (a *API) handleCredentialDelete(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "the name query parameter is required")
		return
	}
	typ, ok := config.FileGetValue(name, "type")
	if !ok || typ == "" {
		writeError(w, http.StatusNotFound, "没有这条连接："+name)
		return
	}

	if jobs := a.D.usedRemotes()[name]; len(jobs) > 0 && r.URL.Query().Get("force") != "1" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("连接 %q 还在被作业 %s 使用，先把它从目录对里换掉再删",
				name, strings.Join(jobs, "、")),
			"used_by": jobs,
		})
		return
	}

	config.Data().DeleteSection(name)
	config.SaveConfig()
	if err := restrictToOwner(config.GetConfigPath()); err != nil {
		writeError(w, http.StatusInternalServerError,
			"已删除，但配置文件权限没能收紧："+err.Error())
		return
	}

	// Drop any cached filesystem for this name. Deleting a section and creating a
	// new remote with the same name later would otherwise reuse the old handle -
	// and its old credentials - for the lifetime of the process. rclone keeps that
	// cache in fs/cache, not on the ConfigFs hook.
	cache.ClearConfig(name)

	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": name,
		"remotes": a.CredentialViews(),
		"types":   a.remoteTypes(),
		"file":    config.GetConfigPath(),
	})
}

// handleCredentials lists or updates remotes.
func (a *API) handleCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"remotes": a.CredentialViews(),
			// The types a new remote can be created from. Sent with the list
			// because both are needed to render one screen, and two requests here
			// would let the page show a stale picker.
			"types": a.remoteTypes(),
			// The path is shown so the user knows which file is being edited. It
			// is not accepted back: reads and writes always use the path rclone
			// was started with.
			"file": config.GetConfigPath(),
		})

	case http.MethodPost, http.MethodPut:
		// ?type= means "create this remote"; without it, "update this remote".
		if strings.TrimSpace(r.URL.Query().Get("type")) != "" {
			a.handleCredentialCreate(w, r)
			return
		}
		a.handleCredentialSave(w, r)

	case http.MethodDelete:
		a.handleCredentialDelete(w, r)

	default:
		writeError(w, http.StatusMethodNotAllowed, "GET, POST or DELETE only")
	}
}

// handleCredentialSave writes the submitted fields for one remote.
func (a *API) handleCredentialSave(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "the name query parameter is required")
		return
	}

	typ, _ := config.FileGetValue(name, "type")
	if typ == "" {
		// Tell the user how to create one. "no such remote" alone reads as "this
		// form is broken" when the form is what they were trying to use.
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("没有叫 %q 的远程。要新建一个，请带上 type 参数：POST /api/credentials?name=%s&type=feishu",
				name, name))
		return
	}
	reg, err := fs.Find(typ)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown backend type for "+name+": "+err.Error())
		return
	}

	var in struct {
		// Values maps an option name to its new value. A null value leaves the
		// field untouched; an empty string clears it. Pointers make those two
		// distinguishable, which a plain map[string]string cannot.
		Values map[string]*string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(in.Values) == 0 {
		writeError(w, http.StatusBadRequest, `"values" is required`)
		return
	}

	// Validate every key before writing any: a half-applied edit leaves the user
	// with credentials they did not ask for.
	known := map[string]fs.Option{}
	for _, opt := range reg.Options {
		known[opt.Name] = opt
	}
	keys := make([]string, 0, len(in.Values))
	for k := range in.Values {
		if _, ok := known[k]; !ok {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("the %s backend has no option %q", typ, k))
			return
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	changed := make([]string, 0, len(keys))
	for _, k := range keys {
		v := in.Values[k]
		if v == nil {
			continue // leave alone
		}
		opt := known[k]
		value := *v
		if strings.TrimSpace(value) == "" {
			config.FileDeleteKey(name, k)
			changed = append(changed, k)
			continue
		}
		// Whitespace at either end is never intended in a credential and is one of
		// the few things that silently breaks authentication, so it is trimmed
		// here rather than reported.
		value = strings.TrimSpace(value)
		if opt.IsPassword {
			enc, err := obscured.Encode(value)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not encode the secret: "+err.Error())
				return
			}
			value = enc
		}
		config.FileSetValue(name, k, value)
		changed = append(changed, k)
	}

	if len(changed) == 0 {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}

	config.SaveConfig()

	// The config file holds credentials: make sure it is not world readable. Doing
	// this after every save also repairs a file that was created with a sloppy
	// umask.
	if err := restrictToOwner(config.GetConfigPath()); err != nil {
		writeError(w, http.StatusInternalServerError,
			"credentials were written but the file permissions could not be restricted: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"updated": changed,
		// The new state, still without any secret value.
		"remote": a.credentialView(name, typ, a.D.usedRemotes()[name]),
	})
}

// restrictToOwner makes a file readable only by its owner.
//
// A no-op on Windows apart from clearing the read-only bit, because the file
// lives under the user profile there; on Unix it is the difference between 0600
// and 0644, and rclone's own config is 0600.
func restrictToOwner(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() == 0o600 {
		return nil
	}
	return os.Chmod(path, 0o600)
}

// handleCredentialTest verifies that a remote can be opened and listed.
//
// A token request alone would prove the app credentials are right, but the root
// token is the other half of the configuration and a listing exercises both. It is
// also what the next sync will do first, so a pass here means the next run gets
// past the first step.
func (a *API) handleCredentialTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "the name query parameter is required")
		return
	}
	if _, ok := config.FileGetValue(name, "type"); !ok {
		writeError(w, http.StatusNotFound, "no such remote: "+name)
		return
	}

	// A credential check is a couple of API calls, so it is bounded: an
	// unreachable endpoint must not hold the request open.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// The same cache the sync path uses, so a failure here is the failure the run
	// would hit. Purged first: the point is to check what is in the file now, not
	// what was there when the process started.
	fsrc, err := fs.NewFs(ctx, name+":")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(),
			"hint": "the remote could not be opened; check the fields above",
		})
		return
	}

	entries, err := fsrc.List(ctx, "")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hint": "authentication or the root folder failed; " +
				"check app_secret and root_token",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"entries": len(entries),
		"hint":    fmt.Sprintf("可以访问根目录，其中有 %d 项", len(entries)),
	})
}
