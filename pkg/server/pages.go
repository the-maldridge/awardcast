package server

import (
	"net/http"

	"github.com/the-maldridge/webapp"
	"github.com/the-maldridge/webapp/auth"
)

const (
	// loginTemplate belongs to the theme, which is why it is not namespaced
	// under awardcast: the sign-in box is the framework's, with the field names
	// its handler reads baked into it.
	loginTemplate   = "@core/login.p2"
	presentTemplate = "@awardcast/present.p2"
	landingTemplate = "@awardcast/views/admin/landing.p2"

	// adminEntry is where a sign-in that named no destination lands.
	adminEntry = "/admin/"

	// credentialsRejected is the one message every failed sign-in gets. Which
	// half was wrong is what an enumeration attack reads, so this application
	// does not say, and neither does the service behind it.
	credentialsRejected = "Those credentials were not accepted."
)

// uiViewLogin serves both halves of the sign-in exchange at one path, because
// the theme's sign-in link is /login and its form posts to that same path. The
// submit handler answers anything but POST with 403 and an Allow header, which
// is the right answer for the methods in between.
func (u *ui) uiViewLogin(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return u.uiViewLoginPage(w, r)
	}

	var (
		rejected   bool
		wantNext   string
		wantName   string
		failedHere = func(_ http.ResponseWriter, r *http.Request, next string, _ error) {
			rejected = true
			wantNext = next
			wantName, _ = webapp.FormString(r, "username")
		}
	)

	u.auth.LoginHandler(auth.LoginOptions{
		Fallback:  adminEntry,
		OnFailure: failedHere,
	})(w, r)
	if !rejected {
		return nil
	}

	// The failure comes back as the form rather than a bodyless 401: whoever is
	// standing at the sign-in box needs the field back, and a client that posted
	// a bad credential must not be served a 200 on the way to getting it.
	//
	// The reason goes in under loginError, the name the theme's sign-in box
	// reads, as well as in Reasons, which is what the log line is built from.
	// Both carry the one value on purpose: which half of the credential was
	// wrong stays unsaid either way.
	return &webapp.Rejected{
		Template: loginTemplate,
		Status:   http.StatusUnauthorized,
		Values: webapp.Context{
			"action":     "/login",
			"username":   wantName,
			"next":       wantNext,
			"loginError": credentialsRejected,
		},
		Reasons: []string{credentialsRejected},
	}
}

// uiViewLoginPage renders the theme's sign-in form. The destination is read
// through auth's own sanitizer, which is what stops a next pointing at somebody
// else's site.
func (u *ui) uiViewLoginPage(w http.ResponseWriter, r *http.Request) error {
	u.View(w, r, loginTemplate, webapp.Context{
		"action": "/login",
		"next":   auth.SafeNext(r.URL.Query().Get("next"), adminEntry),
	})
	return nil
}

// uiViewPresent renders the screen the room watches. It is deliberately not one
// of the theme's pages: the shared layout brings a navbar, a footer, and a
// sign-out button into what has to be a full-screen document with nothing in
// the way of the award.
func (u *ui) uiViewPresent(w http.ResponseWriter, r *http.Request) error {
	// The wire format is defined once, in winning.go, and rendered into the
	// page. A second copy maintained by hand in the template is what used to
	// decide whether a click moved the projector, and nothing checked it.
	u.View(w, r, presentTemplate, webapp.Context{
		"msgTypeLoad":   msgTypeLoad,
		"msgTypeReveal": msgTypeReveal,
		"msgTypeClear":  msgTypeClear,
	})
	return nil
}

// uiViewAdminLanding is the entry point behind the sign-in, and holds the same
// three lists the navbar does, because a blank page is not an entry point.
func (u *ui) uiViewAdminLanding(w http.ResponseWriter, r *http.Request) error {
	u.View(w, r, landingTemplate, nil)
	return nil
}
