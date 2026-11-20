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
	"/colors/",
	"/colors/add",
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

// TestNavSplitsIntoSectionMenus is the navbar a producer actually looks at: the
// sections are menus of their own, and exactly one of them is lit on any page.
// That last part is the reason each section owns a path segment rather than
// sharing /admin, since the theme decides what is active by comparing the first
// segment of a link with the first segment of the request, and a shared prefix
// lights all of them at once.
func TestNavSplitsIntoSectionMenus(t *testing.T) {
	app := signedInApp(t)

	sections := []struct{ path, menu string }{
		{"/award/", "Awards"},
		{"/award/add", "Awards"},
		{"/award/bulk-add", "Awards"},
		{"/colors/", "Colors"},
		{"/colors/add", "Colors"},
		{"/recipient/", "Recipients"},
		{"/recipient/add", "Recipients"},
		{"/winnings/", "Winnings"},
		{"/winnings/assign", "Winnings"},
	}

	for _, s := range sections {
		t.Run(s.path, func(t *testing.T) {
			body := webapptest.Body(t, app.Get(s.path))

			if got := strings.Count(body, "has-dropdown is-hoverable"); got != 4 {
				t.Errorf("GET %s rendered %d menus, want 4\n%s", s.path, got, body)
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
		{"/colors/add", "color_name=x&color_bg=%23ffff00&color_text=%23000000&color_winner=%23000000&color_bg_dir=to+bottom"},
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

// TestSigninPageHooksItsCentering checks the two ends of the login box's
// centring are joined up here: the page carries the body class the theme's
// stylesheet hangs its centring rules on, and it links that stylesheet at all
// — the no-chrome override means the login page gets no help from the navbar
// pages having it.
func TestSigninPageHooksItsCentering(t *testing.T) {
	app := newTestApp(t)

	page := webapptest.Body(t, app.Get("/login"))
	if !strings.Contains(page, `<body class="wa-login-page"`) {
		t.Errorf("the sign-in page does not carry the body class the theme centres on:\n%s", page)
	}
	if !strings.Contains(page, `href="/static/core/css/theme.css"`) {
		t.Errorf("the sign-in page does not link the stylesheet that centres it:\n%s", page)
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
		"/colors/add",
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
		// A freshly migrated database holds the seeded Classic theme at id 1,
		// which is as good a thing to stand in front of a delete form as any.
		"/colors/1/delete",
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

// colorForm posts a color theme through the very form a producer uses, and
// fails the test if the save did not redirect. The hash signs are %23 because
// a raw one in a query string starts a fragment.
func colorForm(t *testing.T, app *webapptest.App, path, query string) *http.Response {
	t.Helper()

	resp := app.PostForm(path, query)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST %s: got %d, want %d\n%s",
			path, resp.StatusCode, http.StatusSeeOther, webapptest.Body(t, resp))
	}
	return resp
}

// solidThemeForm is a complete solid submission under a name, with colors
// that pass every contrast check so the only thing left to test is the thing
// the test is about.
func solidThemeForm(name string) string {
	return "color_name=" + name +
		"&color_bg=%23123456&color_gradient_off=true" +
		"&color_text=%23ffffff&color_winner=%23ffd700" +
		"&color_bg_dir=to+bottom"
}

// TestColorThemeCRUD walks the whole colors section: the add form, a save,
// the list rendering its swatch, the edit, and both halves of the delete.
func TestColorThemeCRUD(t *testing.T) {
	app := signedInApp(t)

	body := webapptest.Body(t, app.Get("/colors/add"))
	if !strings.Contains(body, `id="color_name"`) {
		t.Errorf("the add form has no name field:\n%s", body)
	}

	colorForm(t, app, "/colors/add", "color_name=Zebra"+
		"&color_bg=%231a1a2e&color_bg_to=%2316213e&color_bg_dir=to+bottom+right"+
		"&color_text=%23ffffff&color_winner=%23ffd700")
	colorForm(t, app, "/colors/add", solidThemeForm("Amber"))

	list := webapptest.Body(t, app.Get("/colors/"))
	if !strings.Contains(list, "Zebra") || !strings.Contains(list, "Amber") {
		t.Errorf("the list is missing a theme it was just given:\n%s", list)
	}
	// The gradient row shows the gradient; a row that stored the direction
	// but lost the second color would paint a stripe instead.
	if !strings.Contains(list, "linear-gradient(to bottom right, #1a1a2e, #16213e)") {
		t.Errorf("the gradient theme did not render its gradient swatch:\n%s", list)
	}
	// Order is by name, and name is unique, so Amber sits before Zebra.
	if strings.Index(list, "Amber") > strings.Index(list, "Zebra") {
		t.Errorf("the list is not in name order:\n%s", list)
	}

	// Amber is id 3: Classic seeds at 1, and the two rows above came in
	// creation order.
	colorForm(t, app, "/colors/3/edit", solidThemeForm("Slate"))
	list = webapptest.Body(t, app.Get("/colors/"))
	if !strings.Contains(list, "Slate") {
		t.Errorf("the edited name is not in the list:\n%s", list)
	}
	if strings.Contains(list, "Amber") {
		t.Errorf("the old name survived the edit, so the update inserted a theme:\n%s", list)
	}

	confirm := webapptest.Body(t, app.Get("/colors/3/delete"))
	if !strings.Contains(confirm, "Slate") {
		t.Errorf("the delete confirmation does not name the theme:\n%s", confirm)
	}

	colorForm(t, app, "/colors/3/delete", "")
	list = webapptest.Body(t, app.Get("/colors/"))
	if strings.Contains(list, "Slate") {
		t.Errorf("the deleted theme is still listed:\n%s", list)
	}
}

// cellText returns the part of a table page from a name up to the end of its
// cell, which is where that row's tags live.
func cellText(body, name string) string {
	at := strings.Index(body, name)
	if at < 0 {
		return ""
	}
	end := strings.Index(body[at:], "</td>")
	if end < 0 {
		return body[at:]
	}
	return body[at : at+end]
}

// TestOneDefaultThemeOnly is the one-default rule from every direction: a new
// claimant takes the mark, the current holder cannot simply walk away from
// it, and the holder cannot be deleted either. Zero defaults is the state
// every later fallback query would have to survive, so the forms refuse to
// create it.
func TestOneDefaultThemeOnly(t *testing.T) {
	app := signedInApp(t)

	// The seeded Classic holds the mark on a fresh database, and refuses to
	// be deleted while it does.
	if got := app.PostForm("/colors/1/delete", "").StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("deleting the default theme: got %d, want %d", got, http.StatusUnprocessableEntity)
	}

	colorForm(t, app, "/colors/add", solidThemeForm("Second")+"&color_default=true")

	list := webapptest.Body(t, app.Get("/colors/"))
	if got := strings.Count(list, "color-default-tag"); got != 1 {
		t.Errorf("the list carries %d default tags, want exactly 1:\n%s", got, list)
	}
	if !strings.Contains(cellText(list, "Second"), "color-default-tag") {
		t.Errorf("the new theme did not take the default mark:\n%s", list)
	}
	if strings.Contains(cellText(list, "Classic"), "color-default-tag") {
		t.Errorf("Classic kept the default mark:\n%s", list)
	}

	// Walking away from the mark is refused, and deleting the holder is
	// refused too.
	if got := app.PostForm("/colors/2/edit", solidThemeForm("Second")).StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("unchecking the current default: got %d, want %d", got, http.StatusUnprocessableEntity)
	}
	if got := app.PostForm("/colors/2/delete", "").StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("deleting the current default: got %d, want %d", got, http.StatusUnprocessableEntity)
	}
}

// TestBootstrapSeedsClassic is the promise an upgrade makes: the first boot
// of the new version puts a Classic theme holding the default on the list,
// and an award with no theme of its own wears the screen that was hard-coded
// before themes existed.
func TestBootstrapSeedsClassic(t *testing.T) {
	app := signedInApp(t)

	list := webapptest.Body(t, app.Get("/colors/"))
	if !strings.Contains(list, "Classic") {
		t.Errorf("a fresh database has no Classic theme:\n%s", list)
	}
	if !strings.Contains(cellText(list, "Classic"), "color-default-tag") {
		t.Errorf("Classic does not hold the default mark:\n%s", list)
	}

	// A bare award — no theme assigned — resolves through the default to the
	// original colors in the presenter's JSON.
	if resp := app.PostForm("/award/add", "award_slug=bare&award_title=Bare&award_sub="); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding an award: got %d", resp.StatusCode)
	}
	if resp := app.PostForm("/recipient/add", "recipient_name=Anyone"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding a recipient: got %d", resp.StatusCode)
	}
	if resp := app.PostForm("/winnings/assign", "award=1&recipient=1"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("assigning the winning: got %d", resp.StatusCode)
	}

	data := webapptest.Body(t, app.Get("/public/winnings/1/data"))
	for _, want := range []string{`"Bg":"#fffff0"`, `"BgTo":""`, `"Text":"#000000"`, `"Winner":"#000000"`} {
		if !strings.Contains(data, want) {
			t.Errorf("the unassigned award's data does not carry %s:\n%s", want, data)
		}
	}
}

// TestWinningDataCarriesTheAwardTheme covers the three resolutions the
// presenter can meet: the award's own theme, no theme (the default), and a
// theme id whose row was deleted (also the default, because deleting a theme
// is allowed to leave awards behind).
func TestWinningDataCarriesTheAwardTheme(t *testing.T) {
	app := signedInApp(t)

	colorForm(t, app, "/colors/add", "color_name=Midnight"+
		"&color_bg=%231a1a2e&color_bg_to=%2316213e&color_bg_dir=to+bottom"+
		"&color_text=%23ffffff&color_winner=%23ffd700")

	for _, slug := range []string{"themed", "plain"} {
		form := "award_slug=" + slug + "&award_title=" + slug + "&award_sub="
		if slug == "themed" {
			form += "&award_theme=2"
		}
		if resp := app.PostForm("/award/add", form); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("adding %s: got %d", slug, resp.StatusCode)
		}
	}
	if resp := app.PostForm("/recipient/add", "recipient_name=Anyone"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding a recipient: got %d", resp.StatusCode)
	}
	for _, pair := range []string{"award=1&recipient=1", "award=2&recipient=1"} {
		if resp := app.PostForm("/winnings/assign", pair); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("assigning a winning: got %d", resp.StatusCode)
		}
	}

	themed := webapptest.Body(t, app.Get("/public/winnings/1/data"))
	for _, want := range []string{`"Name":"Midnight"`, `"Bg":"#1a1a2e"`, `"BgTo":"#16213e"`, `"BgDir":"to bottom"`} {
		if !strings.Contains(themed, want) {
			t.Errorf("the themed award's data does not carry %s:\n%s", want, themed)
		}
	}

	plain := webapptest.Body(t, app.Get("/public/winnings/2/data"))
	if !strings.Contains(plain, `"Name":"Classic"`) {
		t.Errorf("the unassigned award did not resolve to the default:\n%s", plain)
	}

	// Delete the theme out from under the award that wears it: the id
	// dangles, and a dangling id is the default, not an error and not a
	// colorless screen.
	colorForm(t, app, "/colors/2/delete", "")
	after := webapptest.Body(t, app.Get("/public/winnings/1/data"))
	if !strings.Contains(after, `"Name":"Classic"`) {
		t.Errorf("the orphaned award did not fall back to the default:\n%s", after)
	}
}

// TestColorThemesValidateTheirColors is the refusal list: colors that are not
// colors, a gradient without a direction or with an invented one, and text
// the back row cannot read — including the straddle a naive endpoint check
// waves through, where the gradient passes through the text color somewhere
// in the middle.
func TestColorThemesValidateTheirColors(t *testing.T) {
	app := signedInApp(t)

	cases := []struct {
		name, query, wantInBody string
	}{
		{
			name:       "background is not a color",
			query:      "color_name=Oops&color_bg=seventies&color_gradient_off=true&color_text=%23ffffff&color_winner=%23000000",
			wantInBody: "background must be a hex color",
		},
		{
			name:       "gradient without a direction",
			query:      "color_name=Directionless&color_bg=%231a1a2e&color_bg_to=%2316213e&color_bg_dir=&color_text=%23ffffff&color_winner=%23ffd700",
			wantInBody: "not one of the eight",
		},
		{
			name:       "invented direction",
			query:      "color_name=Sideways&color_bg=%231a1a2e&color_bg_to=%2316213e&color_bg_dir=to+nowhere&color_text=%23ffffff&color_winner=%23ffd700",
			wantInBody: "not one of the eight",
		},
		{
			name:       "text melts into the background",
			query:      "color_name=Wash&color_bg=%23fffff0&color_gradient_off=true&color_text=%23777777&color_winner=%23000000",
			wantInBody: "contrasts by",
		},
		{
			// #767676 and #757575 clear the 4.5 ratio against both pure
			// white and pure black by a hair, so only the crossing rule
			// catches a gray on a white-to-black gradient.
			name:       "text crosses the gradient",
			query:      "color_name=Straddle&color_bg=%23ffffff&color_bg_to=%23000000&color_bg_dir=to+bottom&color_text=%23767676&color_winner=%23757575",
			wantInBody: "passes through the award text color",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := app.PostForm("/colors/add", c.query)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("got %d, want %d\n%s", resp.StatusCode,
					http.StatusUnprocessableEntity, webapptest.Body(t, resp))
			}
			if body := webapptest.Body(t, resp); !strings.Contains(body, c.wantInBody) {
				t.Errorf("the refusal does not say %q:\n%s", c.wantInBody, body)
			}
		})
	}

	// None of the refusals wrote anything.
	if got := strings.Count(webapptest.Body(t, app.Get("/colors/")), "<tr>") - 1; got != 1 {
		t.Errorf("%d themes in the list after five refusals, want the one seeded Classic", got)
	}

	// A gradient saved solid: the checkbox outranks the stale value the form
	// still carries, and the stored row loses the gradient completely.
	colorForm(t, app, "/colors/add", "color_name=Fade"+
		"&color_bg=%231a1a2e&color_bg_to=%2316213e&color_bg_dir=to+bottom+right"+
		"&color_text=%23ffffff&color_winner=%23ffd700")
	colorForm(t, app, "/colors/2/edit", "color_name=Fade"+
		"&color_bg=%231a1a2e&color_bg_to=%2316213e&color_bg_dir=to+bottom+right&color_gradient_off=true"+
		"&color_text=%23ffffff&color_winner=%23ffd700")

	list := webapptest.Body(t, app.Get("/colors/"))
	if strings.Contains(list, "linear-gradient(to bottom right") {
		t.Errorf("the theme saved as solid kept its gradient:\n%s", list)
	}
}

// TestAwardThemeAssignmentRoundTrip drives the select on the award form: a
// choice round-trips through the edit form, an empty choice means the
// default, and a theme that is not in the list is a refusal rather than a
// save that quietly drops the choice.
func TestAwardThemeAssignmentRoundTrip(t *testing.T) {
	app := signedInApp(t)

	colorForm(t, app, "/colors/add", solidThemeForm("Brand"))

	colorForm(t, app, "/award/add", "award_slug=branded&award_title=Branded&award_sub=&award_theme=2")

	// The edit form must come back with the chosen theme selected, not the
	// producer's choice silently reset to Default.
	edit := webapptest.Body(t, app.Get("/award/1/edit"))
	if !strings.Contains(edit, `<option value="2" selected>`) {
		t.Errorf("the award form did not select the assigned theme:\n%s", edit)
	}

	list := webapptest.Body(t, app.Get("/award/"))
	if !strings.Contains(list, "Brand") {
		t.Errorf("the award list does not show the assigned theme:\n%s", list)
	}

	colorForm(t, app, "/award/1/edit", "award_slug=branded&award_title=Branded&award_sub=&award_theme=")
	if edit := webapptest.Body(t, app.Get("/award/1/edit")); strings.Contains(edit, `<option value="2" selected>`) {
		t.Errorf("the cleared theme is still selected:\n%s", edit)
	}
	if list := webapptest.Body(t, app.Get("/award/")); !strings.Contains(list, "(default)") {
		t.Errorf("the cleared award does not read as wearing the default:\n%s", list)
	}

	// A theme id that is not on the list — a second tab holding a page from
	// before a delete — comes back as a refusal, not as an award wearing
	// something else.
	if got := app.PostForm("/award/add", "award_slug=ghost&award_title=Ghost&award_sub=&award_theme=999").StatusCode; got != http.StatusUnprocessableEntity {
		t.Errorf("assigning an unknown theme: got %d, want %d", got, http.StatusUnprocessableEntity)
	}
}
