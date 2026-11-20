package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/the-maldridge/webapp/db"
	"github.com/the-maldridge/webapp/webapptest"

	"github.com/the-maldridge/awardcast/pkg/types"
)

// newTestApp builds an application with awardcast's screens mounted on it.
//
// The pieces are wired in the order a real boot uses, with one substitution:
// the identity service is the test harness's own, over a local account store,
// rather than the htpasswd file the binary reads from its working directory.
// Install keeps an already-installed service precisely so this stays possible.
func newTestApp(t *testing.T) *webapptest.App {
	t.Helper()

	opts, err := Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}

	app := webapptest.NewApp(t, opts...)
	app.InstallAuth()
	if err := Install(app.App); err != nil {
		t.Fatalf("Install: %v", err)
	}
	return app
}

// signedInApp is every test that reaches an admin screen.
//
// The GET is not wasted: a CSRF token only exists once a response has offered
// one, and a session minted straight into the cookie jar has never seen a page.
// Skipping this would have every write below arrive without a token and come
// back 403, which is the middleware working and reads exactly like it failing.
func signedInApp(t *testing.T) *webapptest.App {
	t.Helper()

	app := newTestApp(t)
	app.SignIn("producer")

	resp := app.Get("/admin/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("warming up a signed-in session: got %d, want %d",
			resp.StatusCode, http.StatusOK)
	}
	if app.CSRFToken() == "" {
		t.Fatal("the signed-in session holds no CSRF token")
	}
	return app
}

// upload posts a multipart form holding one file under field, with the CSRF
// token the client holds. The helper exists because neither PostForm nor Post
// can express "a file plus ordinary fields" on its own.
func upload(t *testing.T, app *webapptest.App, path, field, fileName string, content []byte) *http.Response {
	t.Helper()

	body, contentType, err := multipartBody(field, fileName, content, app.CSRFToken())
	if err != nil {
		t.Fatalf("building the upload: %v", err)
	}
	return app.Post(path, contentType, body)
}

func multipartBody(field, fileName string, content []byte, csrf string) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if csrf != "" {
		if err := w.WriteField("csrf_token", csrf); err != nil {
			return nil, "", err
		}
	}

	part, err := w.CreateFormFile(field, fileName)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// The public address of every admin screen, kept in one list so a route that
// gets renamed or dropped shows up as a failing test rather than an untested
// hole in the access control.
var adminPages = []string{
	"/admin/",
	"/award/",
	"/award/add",
	"/award/bulk-add",
	"/recipient/",
	"/recipient/add",
	"/recipient/bulk-add",
	"/winnings/",
	"/winnings/assign",
}

func TestAdminPagesRequireAnIdentity(t *testing.T) {
	app := newTestApp(t)

	for _, path := range adminPages {
		t.Run(path, func(t *testing.T) {
			resp := app.Get(path)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET %s anonymous: got %d, want %d",
					path, resp.StatusCode, http.StatusUnauthorized)
			}
		})
	}
}

func TestAdminPagesRenderForSignedInUser(t *testing.T) {
	app := signedInApp(t)

	for _, path := range adminPages {
		t.Run(path, func(t *testing.T) {
			resp := app.Get(path)
			body := webapptest.Body(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s: got %d, want %d\n%s",
					path, resp.StatusCode, http.StatusOK, body)
			}
			// Every page is drawn through the theme's layout, so the marker
			// it always emits is the check that the template namespace
			// resolved and the page actually rendered.
			if !strings.Contains(body, "<main") {
				t.Errorf("GET %s did not render a page inside the layout:\n%s", path, body)
			}
		})
	}
}

// TestNavSplitsIntoThreeMenus is the navbar a producer actually looks at: the
// three sections are menus of their own, and exactly one of them is lit on any
// page. That last part is the reason each section owns a path segment rather than
// sharing /admin, since the theme decides what is active by comparing the first
// segment of a link with the first segment of the request, and a shared prefix
// lights all three at once.
func TestNavSplitsIntoThreeMenus(t *testing.T) {
	app := signedInApp(t)

	sections := []struct{ path, menu string }{
		{"/award/", "Awards"},
		{"/award/add", "Awards"},
		{"/award/bulk-add", "Awards"},
		{"/recipient/", "Recipients"},
		{"/recipient/add", "Recipients"},
		{"/winnings/", "Winnings"},
		{"/winnings/assign", "Winnings"},
	}

	for _, s := range sections {
		t.Run(s.path, func(t *testing.T) {
			body := webapptest.Body(t, app.Get(s.path))

			if got := strings.Count(body, "has-dropdown is-hoverable"); got != 3 {
				t.Errorf("GET %s rendered %d menus, want 3\n%s", s.path, got, body)
			}
			if got := strings.Count(body, "navbar-link is-active"); got != 1 {
				t.Errorf("GET %s lit %d menus, want 1\n%s", s.path, got, body)
			}
			want := `<span class="navbar-link is-active">` + s.menu + `</span>`
			if !strings.Contains(body, want) {
				t.Errorf("GET %s did not light the %s menu:\n%s", s.path, s.menu, body)
			}
		})
	}
}

// TestLandingPageLightsNoMenu records the one thing the split costs: the landing
// page belongs to no section, so no menu is lit while it is on screen.
func TestLandingPageLightsNoMenu(t *testing.T) {
	app := signedInApp(t)

	body := webapptest.Body(t, app.Get("/admin/"))
	if got := strings.Count(body, "navbar-link is-active"); got != 0 {
		t.Errorf("the landing page lit %d menus, want 0\n%s", got, body)
	}
}

// TestMovedAdminAddressesRedirect covers the addresses the three sections had
// before they were broken out. A bookmark and a page rendered by the previous
// build both have to arrive rather than meet a 404 the moment the binary is
// replaced.
func TestMovedAdminAddressesRedirect(t *testing.T) {
	app := signedInApp(t)

	moved := []struct{ from, to string }{
		{"/admin/award/", "/award/"},
		{"/admin/award/1/edit", "/award/1/edit"},
		{"/admin/recipient/bulk-add", "/recipient/bulk-add"},
		{"/admin/winnings/assign", "/winnings/assign"},
		{"/admin/winnings/3/present", "/winnings/3/present"},
	}

	for _, m := range moved {
		t.Run(m.from, func(t *testing.T) {
			resp := app.Get(m.from)
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("GET %s: got %d, want %d\n%s", m.from, resp.StatusCode,
					http.StatusSeeOther, webapptest.Body(t, resp))
			}
			if got := resp.Header.Get("Location"); got != m.to {
				t.Errorf("GET %s: Location %q, want %q", m.from, got, m.to)
			}
		})
	}

	// A doubled slash in the middle of a path must not survive as one at the
	// front of a Location: a browser reads that as a host, and the redirect would
	// have taken a signed-in producer off site.
	resp := app.Get("/admin/award//evil.com/")
	if got := resp.Header.Get("Location"); got != "/award/evil.com/" {
		t.Errorf("Location %q, want %q", got, "/award/evil.com/")
	}

	// A path that never lived here is not a section that moved, and bouncing it
	// somewhere would hide the 404 somebody is looking for.
	if got := app.Get("/admin/reports").StatusCode; got != http.StatusNotFound {
		t.Errorf("GET /admin/reports: got %d, want %d", got, http.StatusNotFound)
	}

	// The old addresses guard what they always guarded.
	if got := newTestApp(t).Get("/admin/award/").StatusCode; got != http.StatusUnauthorized {
		t.Errorf("GET /admin/award/ anonymous: got %d, want %d", got, http.StatusUnauthorized)
	}
}

// TestWriteRoutesRejectAnUnsignedForm covers the CSRF rule from the client's
// side: a signed-in browser that omits the token gets refused. This is the
// protection the previous version had none of.
func TestWriteRoutesRejectAnUnsignedForm(t *testing.T) {
	app := signedInApp(t)

	writes := []struct {
		path, body string
	}{
		{"/award/add", "award_slug=x&award_title=X&award_sub="},
		{"/award/bulk-add", ""},
		{"/recipient/add", "recipient_name=x"},
		{"/recipient/bulk-add", ""},
		{"/winnings/assign", "award=1&recipient=1"},
	}

	for _, w := range writes {
		t.Run(w.path, func(t *testing.T) {
			resp := app.PostFormNoCSRF(w.path, w.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s without a token: got %d, want %d",
					w.path, resp.StatusCode, http.StatusForbidden)
			}
		})
	}
}

// TestAwardIsCreatedThenEdited covers the add form and the edit form in one
// pass, because they are the same template and the only difference between them
// is whether the path carried an id.
func TestAwardIsCreatedThenEdited(t *testing.T) {
	app := signedInApp(t)

	resp := app.PostForm("/award/add", "award_slug=best-dressed&award_title=Best+Dressed&award_sub=Senior+Year")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("creating an award: got %d, want %d\n%s",
			resp.StatusCode, http.StatusSeeOther, webapptest.Body(t, resp))
	}
	if got := resp.Header.Get("Location"); got != "/award/" {
		t.Errorf("creating an award: Location %q, want %q", got, "/award/")
	}

	list := webapptest.Body(t, app.Follow(resp))
	if !strings.Contains(list, "best-dressed") {
		t.Errorf("the new award is not in the list:\n%s", list)
	}

	// The row's id is 1 on a freshly migrated database, which is what the
	// edit path needs. Reaching through the database directly would test the
	// upsert without testing that the list and the path agree.
	resp = app.PostForm("/award/1/edit", "award_slug=best-dressed&award_title=Best+Dressed&award_sub=Grad+Year")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("editing an award: got %d, want %d\n%s",
			resp.StatusCode, http.StatusSeeOther, webapptest.Body(t, resp))
	}

	edited := webapptest.Body(t, app.Follow(resp))
	if !strings.Contains(edited, "Grad Year") {
		t.Errorf("the edited subtitle is not in the list:\n%s", edited)
	}
	// An update that inserted a second row instead would leave the old
	// subtitle behind, which is the failure mode of an upsert keyed wrong.
	if strings.Contains(edited, "Senior Year") {
		t.Errorf("the pre-edit subtitle is still listed, so the update created a new award:\n%s", edited)
	}
}

// TestAwardIDFailures separates a malformed id from an unknown one: "abc" is
// the caller's mistake about the format and 9999 is a stale bookmark, and the
// two have to be distinguishable in the log if nowhere else.
func TestAwardIDFailures(t *testing.T) {
	app := signedInApp(t)

	cases := []struct {
		path string
		want int
	}{
		{"/award/abc/edit", http.StatusBadRequest},
		{"/award/abc/delete", http.StatusBadRequest},
		{"/award/9999/edit", http.StatusNotFound},
		{"/award/9999/delete", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp := app.Get(tc.path)
			if resp.StatusCode != tc.want {
				t.Errorf("GET %s: got %d, want %d", tc.path, resp.StatusCode, tc.want)
			}
		})
	}
}

// TestDeletingAnAwardTakesItsWinnings is the reason the delete runs in a
// transaction: a winning left pointing at a deleted award renders a line with
// no award on the screen the room is watching.
func TestDeletingAnAwardTakesItsWinnings(t *testing.T) {
	app := signedInApp(t)

	seed(t, app)

	resp := app.PostForm("/award/1/delete", "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("deleting an award: got %d, want %d\n%s",
			resp.StatusCode, http.StatusSeeOther, webapptest.Body(t, resp))
	}

	// The winning is gone with it, which the presenter's JSON endpoint says
	// outright.
	if got := app.Get("/public/winnings/1/data").StatusCode; got != http.StatusNotFound {
		t.Errorf("the winning survived its award: GET data got %d, want %d", got, http.StatusNotFound)
	}
	// And the recipient it named is untouched.
	if got := app.Get("/recipient/1/edit").StatusCode; got != http.StatusOK {
		t.Errorf("the recipient should not have been deleted: got %d, want %d", got, http.StatusOK)
	}
}

// seed puts one award, one recipient, and the winning between them in the
// database, through the same forms a producer uses.
func seed(t *testing.T, app *webapptest.App) {
	t.Helper()

	if resp := app.PostForm("/award/add", "award_slug=seed-award&award_title=Seed&award_sub="); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("seeding an award: got %d\n%s", resp.StatusCode, webapptest.Body(t, resp))
	}
	if resp := app.PostForm("/recipient/add", "recipient_name=Seed+Person"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("seeding a recipient: got %d\n%s", resp.StatusCode, webapptest.Body(t, resp))
	}
	if resp := app.PostForm("/winnings/assign", "award=1&recipient=1"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("seeding a winning: got %d\n%s", resp.StatusCode, webapptest.Body(t, resp))
	}
}

// TestWinningDataIsJSON covers the one endpoint the presenter polls. It is
// public on purpose, so it is checked both signed out and for its content type,
// which the page's JSON.parse depends on.
func TestWinningDataIsJSON(t *testing.T) {
	app := signedInApp(t)
	seed(t, app)

	resp := app.Get("/public/winnings/1/data")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET winning data: got %d, want %d\n%s",
			resp.StatusCode, http.StatusOK, webapptest.Body(t, resp))
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type %q, want application/json", got)
	}

	body := webapptest.Body(t, resp)
	for _, want := range []string{"Seed", "Seed Person"} {
		if !strings.Contains(body, want) {
			t.Errorf("winning data does not mention %q:\n%s", want, body)
		}
	}

	if got := app.Get("/public/winnings/4242/data").StatusCode; got != http.StatusNotFound {
		t.Errorf("GET data for an unknown winning: got %d, want %d", got, http.StatusNotFound)
	}
}

// TestPublicRoutesNeedNoIdentity is the other half of the access control: the
// projector screen is in a room full of people who have not signed in.
func TestPublicRoutesNeedNoIdentity(t *testing.T) {
	app := newTestApp(t)

	resp := app.Get("/public/present")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /public/present: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// The presenter is a standalone document: no navbar, no sign-out form.
	body := webapptest.Body(t, resp)
	if strings.Contains(body, "navbar") {
		t.Errorf("the presenter page picked up the theme's navigation:\n%s", body)
	}
	if !strings.Contains(body, "/static/awards/present.css") {
		t.Errorf("the presenter page is not loading its own stylesheet:\n%s", body)
	}
	// A hard-wired ws:// would be refused outright by a browser loading this
	// page over https.
	if !strings.Contains(body, "'https:'") {
		t.Errorf("the presenter page does not choose its socket scheme from the page protocol:\n%s", body)
	}
}

func TestRootRedirectsToPresenter(t *testing.T) {
	app := newTestApp(t)

	resp := app.Get("/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /: got %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if got := resp.Header.Get("Location"); got != "/public/present" {
		t.Errorf("GET /: Location %q, want %q", got, "/public/present")
	}
}

func TestPresenterAssetsAreServed(t *testing.T) {
	app := newTestApp(t)

	for _, path := range []string{
		"/static/awards/present.css",
		"/static/awards/mustache.js",
		"/static/awards/reconnecting-websocket.js",
	} {
		t.Run(path, func(t *testing.T) {
			resp := app.Get(path)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s: got %d, want %d", path, resp.StatusCode, http.StatusOK)
			}
			if body := webapptest.Body(t, resp); body == "" {
				t.Errorf("GET %s served an empty body", path)
			}
		})
	}
}

// TestPresenterCarriesTheWireFormat is the check the hand-copied constants
// never had. The presenter page takes its three message-type numbers from the
// same Go constants that publish them, so a template edit or a broken render
// that leaves a hole where a number should be fails here rather than silently
// turning every button on the admin list into a fetch the page ignores.
func TestPresenterCarriesTheWireFormat(t *testing.T) {
	app := newTestApp(t)

	body := webapptest.Body(t, app.Get("/public/present"))
	for _, want := range []string{
		"const MsgTypeLoad = " + strconv.FormatUint(uint64(msgTypeLoad), 10) + ";",
		"const MsgTypeReveal = " + strconv.FormatUint(uint64(msgTypeReveal), 10) + ";",
		"const MsgTypeClear = " + strconv.FormatUint(uint64(msgTypeClear), 10) + ";",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the presenter page does not carry %q, so it and the server "+
				"no longer agree on the wire format", want)
		}
	}
}

// TestThemeStylesheetIsLinkedAndServed pins the other half of the theme's
// sticky footer: base.p2 must link the theme's own stylesheet and the asset
// must actually arrive. The framework refuses to start a theme whose base
// links a missing file, so this mostly guards the serving half — the page
// linking a stylesheet that answers 404 renders fine and floats its footer.
// The check runs on the anonymous 401 page, which is a themed page through
// the shared layout and needs no identity.
func TestThemeStylesheetIsLinkedAndServed(t *testing.T) {
	app := newTestApp(t)

	resp := app.Get("/static/core/css/theme.css")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/core/css/theme.css: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if body := webapptest.Body(t, resp); !strings.Contains(body, "min-height") {
		t.Errorf("the theme stylesheet arrived without its footer rule:\n%s", body)
	}

	page := webapptest.Body(t, app.Get("/award/"))
	if !strings.Contains(page, `href="/static/core/css/theme.css"`) {
		t.Errorf("a page rendered through the theme layout does not link its stylesheet:\n%s", page)
	}
}

func TestBulkAwardImport(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
		// contains is checked in the response body, which for a rejection is
		// the rendered form carrying the reason.
		contains string
	}{
		{
			name:    "a good file imports",
			content: "Slug,Title,Subtitle\nBest.Dressed,Best Dressed,Senior Year\n",
			want:    http.StatusSeeOther,
		},
		{
			name:     "a short row refuses the file",
			content:  "Slug,Title,Subtitle\nonly-one-value\n",
			want:     http.StatusUnprocessableEntity,
			contains: "row 2",
		},
		{
			name:     "a header with nothing under it says so",
			content:  "Slug,Title,Subtitle\n",
			want:     http.StatusUnprocessableEntity,
			contains: "nothing else",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := signedInApp(t)

			resp := upload(t, app, "/award/bulk-add", "awards_file", "awards.csv", []byte(tc.content))
			if resp.StatusCode != tc.want {
				t.Fatalf("got %d, want %d\n%s", resp.StatusCode, tc.want, webapptest.Body(t, resp))
			}
			if tc.contains != "" {
				if body := webapptest.Body(t, resp); !strings.Contains(body, tc.contains) {
					t.Errorf("response does not mention %q:\n%s", tc.contains, body)
				}
			}
		})
	}
}

// TestBulkAwardImportMatchesRecipientsByHeader is the contract the CSV helper
// implements: columns are named by the header rather than fixed by position.
func TestBulkAwardImportMatchesRecipientsByHeader(t *testing.T) {
	app := signedInApp(t)

	resp := upload(t, app, "/recipient/bulk-add", "recipients_file", "people.csv",
		[]byte("Name\nAda\nGrace\n"))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("got %d, want %d\n%s",
			resp.StatusCode, http.StatusSeeOther, webapptest.Body(t, resp))
	}

	body := webapptest.Body(t, app.Follow(resp))
	for _, want := range []string{"Ada", "Grace"} {
		if !strings.Contains(body, want) {
			t.Errorf("the imported recipient %q is not listed:\n%s", want, body)
		}
	}
}

// TestBulkUploadMissingFile is the difference between a form submitted empty
// and a request that was refused: the first is a re-render telling the producer
// to choose a file.
func TestBulkUploadMissingFile(t *testing.T) {
	app := signedInApp(t)

	resp := app.PostForm("/award/bulk-add", "")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want %d\n%s",
			resp.StatusCode, http.StatusUnprocessableEntity, webapptest.Body(t, resp))
	}
}

// TestBulkUploadLargerThanTheFrameworkDefaultIsAccepted is why the application
// raises the body cap at all: the framework's default is a megabyte, and a
// member list for any real convention is bigger than that.
func TestBulkUploadLargerThanTheFrameworkDefaultIsAccepted(t *testing.T) {
	app := signedInApp(t)

	// Roughly two megabytes, and malformed on the second row so the import
	// stops without touching the database twice. What is under test is that
	// the request survived the body cap, which a 413 would say plainly.
	var buf strings.Builder
	buf.WriteString("Slug,Title,Subtitle\n")
	pad := strings.Repeat("x", 256)
	for buf.Len() < 2<<20 {
		buf.WriteString(pad)
		buf.WriteString("\n")
	}

	resp := upload(t, app, "/award/bulk-add", "awards_file", "big.csv", []byte(buf.String()))
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("a 2MB upload hit the body cap; WithMaxBodyBytes is not taking effect")
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
}

// TestLoginWithBadCredentialsKeepsTheForm covers the one place the application
// renders the theme's own login page after a failure: the status has to say 401
// and the page has to still be the form, with the reason shown.
func TestLoginWithBadCredentialsKeepsTheForm(t *testing.T) {
	app := newTestApp(t)

	// A GET first, so the client holds a CSRF cookie and the POST is held to
	// it, as a browser's would be.
	if resp := app.Get("/login"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp := app.PostForm("/login", "username=nobody&password=nope")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad credentials: got %d, want %d\n%s",
			resp.StatusCode, http.StatusUnauthorized, webapptest.Body(t, resp))
	}

	body := webapptest.Body(t, resp)
	if !strings.Contains(body, `name="password"`) {
		t.Errorf("the failure response is not the sign-in form:\n%s", body)
	}
	if strings.Contains(body, `value="nope"`) {
		t.Errorf("the failure response put the submitted password back in the form")
	}
	if !strings.Contains(body, "credentials") {
		t.Errorf("the failure response shows no reason:\n%s", body)
	}
}

// TestSigninPageCarriesNoChrome pins the theme fix that took the navbar and
// the footer off the page before sign-in. The navbar offers signed-in sections
// and a sign-out to somebody holding neither an identity nor a session, and
// the footer puts a build line on the one page the public ever sees. Both the
// anonymous GET and the rejected POST are checked, because the failure path
// re-renders the same template through another door and dressing coming back
// through there is the same leak.
func TestSigninPageCarriesNoChrome(t *testing.T) {
	app := newTestApp(t)

	body := webapptest.Body(t, app.Get("/login"))
	if !strings.Contains(body, `name="username"`) {
		t.Fatalf("the sign-in page is not the sign-in form:\n%s", body)
	}
	for _, chrome := range []string{"<nav", "navbar-burger", "navbar-menu", "<footer"} {
		if strings.Contains(body, chrome) {
			t.Errorf("the anonymous sign-in page carries %q", chrome)
		}
	}

	resp := app.PostForm("/login", "username=nobody&password=nope")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad credentials: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	rejected := webapptest.Body(t, resp)
	for _, chrome := range []string{"<nav", "navbar-burger", "navbar-menu", "<footer"} {
		if strings.Contains(rejected, chrome) {
			t.Errorf("the rejected sign-in page carries %q", chrome)
		}
	}
}

// TestLoginPageCarriesATokenIsTheFormsJob checks the CSRF input is actually in
// the rendered forms rather than trusting that it was written into the file.
func TestEveryFormCarriesCSRFToken(t *testing.T) {
	app := signedInApp(t)

	pagesWithForms := []string{
		"/award/add",
		"/award/bulk-add",
		"/recipient/add",
		"/recipient/bulk-add",
		"/winnings/assign",
	}

	for _, path := range pagesWithForms {
		t.Run(path, func(t *testing.T) {
			body := webapptest.Body(t, app.Get(path))
			if !strings.Contains(body, `<input type="hidden" name="csrf_token"`) {
				t.Errorf("%s renders a form with no CSRF input:\n%s", path, body)
			}
		})
	}
}

// TestDeleteConfirmationsCarryToken covers the three delete confirmation
// screens, which are the pages most likely to lose the token because their
// forms are the shortest.
func TestDeleteConfirmationsCarryToken(t *testing.T) {
	app := signedInApp(t)
	seed(t, app)

	for _, path := range []string{
		"/award/1/delete",
		"/recipient/1/delete",
		"/winnings/1/delete",
	} {
		t.Run(path, func(t *testing.T) {
			body := webapptest.Body(t, app.Get(path))
			if !strings.Contains(body, `<input type="hidden" name="csrf_token"`) {
				t.Errorf("%s renders a form with no CSRF input:\n%s", path, body)
			}
		})
	}
}

// TestLogoutIsPostOnly is what stops a prefetching crawler from signing people
// out, and is the one behaviour change a bookmarked GET /logout will feel.
func TestLogoutIsPostOnly(t *testing.T) {
	app := signedInApp(t)

	resp := app.Get("/logout")
	if resp.StatusCode == http.StatusOK {
		t.Errorf("GET /logout was accepted; sign-out must be POST-only")
	}
}

// seedTwoWinnings puts two pairings in the database and returns nothing. The ids
// come out 1 and 2 because a freshly migrated database numbers in creation order,
// which is the same assumption the award edit test makes.
func seedTwoWinnings(t *testing.T, app *webapptest.App) {
	t.Helper()

	pairings := []struct{ slug, person string }{
		{"best-dressed", "Ada Lovelace"},
		{"best-hair", "Grace Hopper"},
	}

	for i, p := range pairings {
		id := strconv.Itoa(i + 1)
		if resp := app.PostForm("/award/add", "award_slug="+p.slug+"&award_title=Award&award_sub="); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("seeding award %s: got %d\n%s", p.slug, resp.StatusCode, webapptest.Body(t, resp))
		}
		if resp := app.PostForm("/recipient/add", "recipient_name="+url.QueryEscape(p.person)); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("seeding recipient %s: got %d\n%s", p.person, resp.StatusCode, webapptest.Body(t, resp))
		}
		if resp := app.PostForm("/winnings/assign", "award="+id+"&recipient="+id); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("assigning %s: got %d\n%s", p.slug, resp.StatusCode, webapptest.Body(t, resp))
		}
	}
}

// TestWinningsListDefaultsToWhatIsLeft is the point of the presented mark: the list
// a producer lands on is the queue that remains, not the whole roster.
func TestWinningsListDefaultsToWhatIsLeft(t *testing.T) {
	app := signedInApp(t)
	seedTwoWinnings(t, app)

	// Presenting loads the award in front of the room; reveal is what names the
	// winner, so a present on its own keeps the row in the queue.
	if got := app.Get("/winnings/1/present").StatusCode; got != http.StatusOK {
		t.Fatalf("presenting: got %d", got)
	}
	loadOnly := webapptest.Body(t, app.Get("/winnings/"))
	for _, want := range []string{"Ada Lovelace", "Grace Hopper"} {
		if !strings.Contains(loadOnly, want) {
			t.Errorf("a present with no reveal took %s out of the default list:\n%s", want, loadOnly)
		}
	}

	if got := app.Get("/winnings/1/reveal").StatusCode; got != http.StatusOK {
		t.Fatalf("revealing: got %d", got)
	}

	left := webapptest.Body(t, app.Get("/winnings/"))
	if strings.Contains(left, "Ada Lovelace") {
		t.Errorf("the revealed winning is still in the default list:\n%s", left)
	}
	if !strings.Contains(left, "Grace Hopper") {
		t.Errorf("the winning still to present is missing from the default list:\n%s", left)
	}
	// One row gone out of two, and the page says so: an empty-looking queue that
	// has been emptied by presenting is a different problem than an unstarted one.
	if !strings.Contains(left, "1 of 2 presented") {
		t.Errorf("the default list does not count what has gone out:\n%s", left)
	}

	shown := webapptest.Body(t, app.Get("/winnings/?presented=presented"))
	if !strings.Contains(shown, "Ada Lovelace") || strings.Contains(shown, "Grace Hopper") {
		t.Errorf("the presented view is not the mirror image of the default:\n%s", shown)
	}

	all := webapptest.Body(t, app.Get("/winnings/?presented=all"))
	for _, want := range []string{"Ada Lovelace", "Grace Hopper"} {
		if !strings.Contains(all, want) {
			t.Errorf("the all view is missing %s:\n%s", want, all)
		}
	}
}

// TestRevealReportsAWinningThatIsNotThere: reveal writes the mark now, so it answers
// for the id in its path the way every other id-taking route does.
func TestRevealReportsAWinningThatIsNotThere(t *testing.T) {
	app := signedInApp(t)
	seedTwoWinnings(t, app)

	if got := app.Get("/winnings/9999/reveal").StatusCode; got != http.StatusNotFound {
		t.Errorf("reveal of an unknown winning: got %d, want %d", got, http.StatusNotFound)
	}
	if got := app.Get("/winnings/abc/reveal").StatusCode; got != http.StatusBadRequest {
		t.Errorf("reveal of a malformed id: got %d, want %d", got, http.StatusBadRequest)
	}
}

// TestUnpresentPutsARowBack is the undo for a reveal that never reached the room.
// The way back is a write with a token like every other write, and it lands on the
// view where the row is still visible.
func TestUnpresentPutsARowBack(t *testing.T) {
	app := signedInApp(t)
	seedTwoWinnings(t, app)

	if got := app.Get("/winnings/1/reveal").StatusCode; got != http.StatusOK {
		t.Fatalf("revealing: got %d", got)
	}

	if got := app.PostFormNoCSRF("/winnings/1/unpresent", "").StatusCode; got != http.StatusForbidden {
		t.Errorf("unpresent without a token: got %d, want %d", got, http.StatusForbidden)
	}

	resp := app.PostForm("/winnings/1/unpresent", "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unpresent: got %d\n%s", resp.StatusCode, webapptest.Body(t, resp))
	}
	if got := resp.Header.Get("Location"); got != "/winnings/?presented=all" {
		t.Errorf("Location %q, want %q", got, "/winnings/?presented=all")
	}

	left := webapptest.Body(t, app.Get("/winnings/"))
	if !strings.Contains(left, "Ada Lovelace") {
		t.Errorf("the row did not come back to the default list:\n%s", left)
	}

	all := webapptest.Body(t, app.Get("/winnings/?presented=all"))
	if strings.Contains(all, `class="tag is-success" data-presented>`) {
		t.Errorf("the row came back still marked presented:\n%s", all)
	}
}

// TestWinningsFilterRejectsWhatItDoesNotOffer: a filter spelling nobody recognises
// has to be refused rather than ignored, because the quiet fallback is a list of
// rows the producer did not ask to look at.
func TestWinningsFilterRejectsWhatItDoesNotOffer(t *testing.T) {
	app := signedInApp(t)

	resp := app.Get("/winnings/?presented=sometimes")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown filter: got %d, want %d\n%s",
			resp.StatusCode, http.StatusBadRequest, webapptest.Body(t, resp))
	}
}

// TestLegacyWinningsStayInTheQueue is the upgrade case. The mark arrived after
// winnings could be assigned, and AutoMigrate adds the column without filling it in,
// so every row written by the previous version has no value for it at all. A filter
// spelled "presented = false" is not true of that, and would have hidden every award
// anybody had already assigned from both filtered views.
func TestLegacyWinningsStayInTheQueue(t *testing.T) {
	app := signedInApp(t)
	seedTwoWinnings(t, app)

	if err := db.G[types.Winning](app.DB).Exec(t.Context(), "update winnings set presented = null"); err != nil {
		t.Fatalf("blanking the mark: %v", err)
	}

	body := webapptest.Body(t, app.Get("/winnings/"))
	for _, want := range []string{"Ada Lovelace", "Grace Hopper"} {
		if !strings.Contains(body, want) {
			t.Errorf("a winning with no value for presented is missing from the default list:\n%s", body)
		}
	}
	if strings.Contains(body, "Nothing assigned yet") {
		t.Errorf("the page says nothing is assigned when two winners are:\n%s", body)
	}

	shown := webapptest.Body(t, app.Get("/winnings/?presented=presented"))
	if strings.Contains(shown, "Ada Lovelace") || strings.Contains(shown, "Grace Hopper") {
		t.Errorf("a row with no mark counts as having gone out:\n%s", shown)
	}
}

// TestListsArriveInTheirNamedOrder pins the ORDER BY on the award and recipient
// lists. Without one a SELECT comes back in insertion order for as long as the
// database is a single SQLite file read by one process, which is every test ever
// run, and the shuffle only shows up in front of a producer. The names are added
// in an order that is neither alphabetical nor reversible, so a list that falls
// back to row order fails the comparison.
func TestListsArriveInTheirNamedOrder(t *testing.T) {
	app := signedInApp(t)

	for _, slug := range []string{"zulu", "alpha", "mike"} {
		form := "award_slug=" + slug + "&award_title=" + slug + "&award_sub="
		if resp := app.PostForm("/award/add", form); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("adding award %q: got %d", slug, resp.StatusCode)
		}
	}
	for _, name := range []string{"Zoe", "Amy", "Mo"} {
		form := "recipient_name=" + name
		if resp := app.PostForm("/recipient/add", form); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("adding recipient %q: got %d", name, resp.StatusCode)
		}
	}

	awards := webapptest.Body(t, app.Get("/award/"))
	positions := []string{"<code>alpha</code>", "<code>mike</code>", "<code>zulu</code>"}
	last := -1
	for _, want := range positions {
		at := strings.Index(awards, want)
		if at < 0 {
			t.Fatalf("award list is missing %s:\n%s", want, awards)
		}
		if at < last {
			t.Errorf("award list is not in slug order, %s comes too early:\n%s", want, awards)
		}
		last = at
	}

	recipients := webapptest.Body(t, app.Get("/recipient/"))
	roster := []string{">Amy</td>", ">Mo</td>", ">Zoe</td>"}
	last = -1
	for _, want := range roster {
		at := strings.Index(recipients, want)
		if at < 0 {
			t.Fatalf("recipient list is missing %s:\n%s", want, recipients)
		}
		if at < last {
			t.Errorf("recipient list is not in name order, %s comes too early:\n%s", want, recipients)
		}
		last = at
	}
}
