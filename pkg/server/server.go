// Package server contains the award show's HTTP application: the admin
// screens a producer drives the ceremony from, the presenter page the room
// watches, and the WebSocket stream that connects a click in one to the other.
package server

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/the-maldridge/authware/backend/htpasswd"
	"github.com/the-maldridge/authware/backend/netauth"
	"github.com/the-maldridge/webapp"
	"github.com/the-maldridge/webapp/auth"
	"github.com/the-maldridge/webapp/db"

	// The Bulma theme registers itself from its init and owns every layout
	// block the admin screens extend.
	_ "github.com/the-maldridge/webapp-bulma"

	"github.com/the-maldridge/awardcast/pkg/types"
)

const (
	// envPrefix namespaces the environment variables the framework reads, so
	// that a host running several of these never shares a database or a
	// session with another.
	envPrefix = "AWARDCAST"

	// templatesNS is where this application's own pages are mounted. "core"
	// belongs to the active theme and "dev" to the debug overlay, so the
	// application needs a namespace of its own.
	templatesNS = "awardcast"

	// staticPath serves the presenter's assets. The theme owns /static/core,
	// so the application's files live under their own prefix.
	staticPath = "/static/awards"

	// adminPrefix is what is left of the address everything used to live under:
	// the landing page a sign-in lands on, and the old section paths, which now
	// redirect to the sections' own top level paths.
	adminPrefix = "/admin"

	// sectionWeight places the three admin sections in the administration band,
	// which the framework numbers 90 and above. They share it on purpose: ties
	// break alphabetically, so what a viewer sees is Awards, Recipients,
	// Winnings whatever order they happen to be registered in.
	sectionWeight = 90

	// envThemeDir replaces the compiled-in templates with a directory on disk,
	// which is how a title gets fixed during a rehearsal without a rebuild.
	//
	// It used to be AWARDCAST_DEBUG. That name now belongs to the framework,
	// which parses <PREFIX>_DEBUG as a boolean and treats a path in it as a
	// startup error.
	envThemeDir = "AWARDCAST_THEME_DIR"

	// envSessionKey and envMechs are authware's names for the session signing
	// key and the colon-separated password backend list, kept so an existing
	// deployment keeps working unchanged.
	envSessionKey = "AUTHWARE_SESSION_KEY"
	envMechs      = "AUTHWARE_BASIC_MECHS"

	// defaultAddr is the address awardcast has always listened on.
	defaultAddr = ":1323"

	// defaultMech is the password backend used when none are named, matching
	// what authware defaulted to.
	defaultMech = "htpasswd"

	// maxUploadBytes bounds a request body. Bulk CSV uploads are POSTs, and
	// the framework caps a body at a megabyte by default, which is smaller
	// than the member list of any convention worth supporting.
	maxUploadBytes = 16 << 20

	// socketTimeout bounds the presenter's connection. Accepting a WebSocket
	// hijacks the connection, and a hijacked connection keeps the read deadline
	// the server set before the handler ran: coder/websocket never clears it.
	// Under the framework's 30 second default the presenter would therefore
	// drop its socket twice a minute, reconnect, and miss whatever was clicked
	// in between, so the timeout is set past the length of any ceremony.
	// It can be lowered again per deployment with AWARDCAST_READ_TIMEOUT and
	// AWARDCAST_WRITE_TIMEOUT.
	socketTimeout = 24 * time.Hour
)

// The environment names this application defaults rather than fixes. They are
// the framework's own names, spelled out so that defaulting one is visibly the
// same setting an operator would override.
const (
	envAddr         = envPrefix + "_ADDR"
	envReadTimeout  = envPrefix + "_READ_TIMEOUT"
	envWriteTimeout = envPrefix + "_WRITE_TIMEOUT"
)

// defaultIfUnset returns the deployment's value for name, or fallback when the
// environment did not name it.
//
// This is how an application offers a default without making a variable inert:
// the framework fills a configuration field from the environment only when no
// option claimed it, so passing our own number unconditionally would mean an
// operator setting the variable changes nothing, and nothing says so.
func defaultIfUnset(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return fallback
}

//go:embed theme
var assets embed.FS

// ui is the handler set. It carries the application so every handler reaches
// the database, the renderer, and the redirect helpers through one receiver,
// and the event stream, which is the only piece of state the application has
// that the framework knows nothing about.
type ui struct {
	*webapp.App

	auth   *auth.Service
	stream *EventStream
	static fs.FS
}

// New builds the application. Options are applied after the application's own,
// so a caller can override the database, the templates, or the environment
// prefix and inherit everything else.
func New(opts ...webapp.Option) (*webapp.App, error) {
	base, err := Options()
	if err != nil {
		return nil, err
	}

	runtime, err := runtimeOptions()
	if err != nil {
		return nil, err
	}

	app, err := webapp.New(append(base, runtime...)...)
	if err != nil {
		return nil, err
	}
	if err := Install(app); err != nil {
		_ = app.Close()
		return nil, err
	}
	return app, nil
}

// Options returns what the framework has to be told before the application is
// built: this application's template namespace, its models, and the body cap a
// bulk upload needs.
//
// It is exported so a test can build an application on a temporary database
// with webapptest and then hand it to Install. Deliberately absent are the
// listening address, the environment prefix, and the timeouts, all of which a
// harness wants to keep its own answer for; those are in runtimeOptions.
func Options() ([]webapp.Option, error) {
	pages, _, err := theme()
	if err != nil {
		return nil, err
	}

	options := []webapp.Option{
		webapp.WithTheme("bulma"),
		webapp.WithSiteTitle("AwardCast"),
		webapp.WithTemplates(templatesNS, pages),
		// Image has no screen of its own yet. Its table is still registered so
		// that a database written by an older version is never rewritten out
		// from under somebody.
		webapp.WithDBOptions(db.WithAutoMigrate(
			&types.Award{},
			&types.Image{},
			&types.Recipient{},
			&types.Winning{},
		)),
		// The framework caps a request body at a megabyte by default, which is
		// smaller than the member list of any convention worth supporting, and
		// bulk import is a feature rather than a tunable. The cap is therefore
		// set here rather than defaulted, and <PREFIX>_MAX_BODY_BYTES is not
		// consulted.
		webapp.WithMaxBodyBytes(maxUploadBytes),
	}

	return options, nil
}

// runtimeOptions are the deployment settings that a constructed application
// needs and a test harness does not want overridden.
func runtimeOptions() ([]webapp.Option, error) {
	t, err := timeouts()
	if err != nil {
		return nil, err
	}

	return []webapp.Option{
		webapp.WithEnvPrefix(envPrefix),
		webapp.WithAddr(defaultIfUnset(envAddr, defaultAddr)),
		// The presenter holds a WebSocket for the length of a ceremony, and a
		// hijacked connection keeps the server's deadlines. See socketTimeout
		// and timeouts.
		webapp.WithTimeouts(t),
	}, nil
}

// timeouts are the server deadlines, raised from the framework's 30 and 45
// second defaults because the presenter's socket would otherwise drop twice a
// minute. See socketTimeout.
//
// The two environment variables are read here rather than left to the
// framework, because WithTimeouts has to be called to raise the defaults at
// all, and the framework only consults the environment for a field no option
// claimed. Parsing them here keeps a deployment's AWARDCAST_READ_TIMEOUT from
// being silently ignored. The idle timeout stays at the framework's default: it
// bounds keep-alive between requests, which a hijacked connection has none of.
func timeouts() (webapp.Timeouts, error) {
	t := webapp.Timeouts{ReadTimeout: socketTimeout, WriteTimeout: socketTimeout}

	for _, field := range []struct {
		name   string
		target *time.Duration
	}{
		{envReadTimeout, &t.ReadTimeout},
		{envWriteTimeout, &t.WriteTimeout},
	} {
		raw, ok := os.LookupEnv(field.name)
		if !ok {
			continue
		}
		d, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return t, fmt.Errorf("%s: %w", field.name, err)
		}
		*field.target = d
	}
	return t, nil
}

// Install mounts this application's screens onto app: the identity service, the
// navbar entries, the route table, and the presenter's static files.
//
// It is separate from New because templates can only be mounted while an
// application is being built, whereas routes, nav, and authentication all need
// an application to attach to. A test therefore passes Options() to a harness of
// its own and calls this; the binary does both in one go.
//
// An application that already has an identity service keeps it. That is what
// lets a test install a service with a known session key and a local account
// backend, rather than depending on the .htpasswd file in whoever's working
// directory the test happens to run from.
func Install(app *webapp.App) error {
	_, static, err := theme()
	if err != nil {
		return err
	}

	svc := app.AuthService()
	if svc == nil {
		if svc, err = installAuth(app); err != nil {
			return err
		}
	}

	u := &ui{App: app, auth: svc, stream: newES(), static: static}
	u.registerNav()
	u.registerRoutes()
	return nil
}

// theme returns the page templates and the static assets. The pages come from
// the binary unless envThemeDir names a directory to read them from instead.
func theme() (pages fs.FS, static fs.FS, err error) {
	if dir, ok := os.LookupEnv(envThemeDir); ok {
		slog.Info("Serving templates from disk", "dir", dir)
		pages = os.DirFS(dir)
	} else if pages, err = fs.Sub(assets, "theme/p2"); err != nil {
		return nil, nil, fmt.Errorf("template dir: %w", err)
	}

	if static, err = fs.Sub(assets, "theme/static"); err != nil {
		return nil, nil, fmt.Errorf("static dir: %w", err)
	}
	return pages, static, nil
}

// installAuth builds the authentication service over the application's own
// database and wires it in.
//
// The order is the only one available: the database is opened by webapp.New, so
// a service that needs it is built afterwards, and its tables have to be part
// of the schema before the first request, which is what RegisterComponent is
// for. Neither auth.New nor InstallAuth migrates anything on its own.
func installAuth(app *webapp.App) (*auth.Service, error) {
	mechs, err := mechanisms()
	if err != nil {
		return nil, err
	}

	svc, err := auth.New(app.DB,
		auth.WithSessionKey(os.Getenv(envSessionKey)),
		auth.WithOnlyMechanisms(mechs...),
	)
	if err != nil {
		return nil, fmt.Errorf("auth service: %w", err)
	}
	if err := app.RegisterComponent(svc); err != nil {
		return nil, fmt.Errorf("auth tables: %w", err)
	}
	if err := app.InstallAuth(svc); err != nil {
		return nil, fmt.Errorf("install auth: %w", err)
	}

	// InstallAuth names the session cookie after the environment prefix. This
	// application predates that and issued authware's plain name, so the name
	// is put back: an upgrade should not sign out whoever is already signed in.
	err = svc.SetCookiePolicy(auth.CookiePolicy{
		Name:     auth.DefaultCookieName,
		Secure:   app.Config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Lifetime: svc.SessionLifetime(),
	})
	if err != nil {
		return nil, fmt.Errorf("session cookie: %w", err)
	}
	return svc, nil
}

// mechanisms resolves the password backends named by AUTHWARE_BASIC_MECHS,
// which is the same colon-separated list of factory names authware's own
// middleware read. It defaults to htpasswd over the .htpasswd and .htgroup
// files in the working directory.
func mechanisms() ([]auth.Mechanism, error) {
	var names []string
	for _, name := range strings.Split(os.Getenv(envMechs), ":") {
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		names = []string{defaultMech}
	}

	factories := map[string]func() (auth.Mechanism, error){
		defaultMech: htpasswd.New,
		"netauth":   netauth.New,
	}

	mechs := make([]auth.Mechanism, 0, len(names))
	for _, name := range names {
		factory, ok := factories[name]
		if !ok {
			return nil, fmt.Errorf("auth mechanism %q: unknown, expected %q or %q",
				name, defaultMech, "netauth")
		}
		m, err := factory()
		if err != nil {
			return nil, fmt.Errorf("auth mechanism %q: %w", name, err)
		}
		mechs = append(mechs, m)
	}
	return mechs, nil
}

// registerNav publishes the three admin sections to the theme's navbar.
//
// Each section is a top-level menu rather than a child of one Admin entry, and
// each therefore owns a path segment of its own: the theme decides what is
// active by comparing the first path segment of a link with the first segment of
// the request, so three menus mounted under /admin would all light up on every
// admin page, and the whole point of highlighting one is that only one is lit.
// The sections share a weight, ties break alphabetically, and so they read
// Awards, Recipients, Winnings regardless of the order these appear in.
//
// None of it carries a Perm, and neither does any route behind it: what a viewer
// may reach is answered by RequireLogin alone, and naming a permission here
// would be a second answer to that question, free to disagree with the first.
func (u *ui) registerNav() {
	u.Nav(
		webapp.NavItem{
			Weight: sectionWeight,
			Label:  "Awards",
			Href:   "/award/",
			Children: []webapp.NavItem{
				{Label: "All awards", Href: "/award/"},
				{Label: "Add an award", Href: "/award/add"},
				{Label: "Import awards from CSV", Href: "/award/bulk-add"},
			},
		},
		webapp.NavItem{
			Weight: sectionWeight,
			Label:  "Recipients",
			Href:   "/recipient/",
			Children: []webapp.NavItem{
				{Label: "All recipients", Href: "/recipient/"},
				{Label: "Add a recipient", Href: "/recipient/add"},
				{Label: "Import recipients from CSV", Href: "/recipient/bulk-add"},
			},
		},
		webapp.NavItem{
			Weight: sectionWeight,
			Label:  "Winnings",
			Href:   "/winnings/",
			Children: []webapp.NavItem{
				{Label: "All winnings", Href: "/winnings/"},
				{Label: "Assign a winner", Href: "/winnings/assign"},
			},
		},
	)
}

// registerRoutes is the whole route table. Public paths are unchanged from the
// version this replaced, so a saved presenter bookmark still lands.
func (u *ui) registerRoutes() {
	// The root of the site is the screen the room is watching.
	u.HandleErr("/", func(w http.ResponseWriter, r *http.Request) error {
		u.SeeOther(w, r, "/public/present")
		return nil
	})

	// The presenter page is driven by the event stream and fetches the details
	// of one winning at a time. None of it carries a session: routes are open
	// unless wrapped in a guard, so these are left unguarded deliberately, and
	// the block comment above is the record of that decision.
	u.Route("/public", func(r webapp.Router) {
		r.Get("/e", r.Public(u.stream.Handler))
		r.GetErr("/present", u.uiViewPresent)
		r.Route("/winnings", func(r webapp.Router) {
			r.GetErr("/{id}/data", u.uiViewWinningData)
		})
	})

	// The theme's sign-in link points at /login and its form posts to it too,
	// so one pattern serves both halves. The auth package's submit handler
	// answers anything but POST with 403 and an Allow header.
	u.HandleErr("/login", u.uiViewLogin)

	// Sign-out is POST-only, which is why the GET /logout of the previous
	// version is gone; the theme's navbar posts to this path.
	u.Handle("/logout", u.auth.LogoutHandler("/"))

	// Presenter assets: the vendored mustache and reconnecting WebSocket
	// libraries and the stylesheet the award animation lives in.
	u.Mount(staticPath, http.StripPrefix(staticPath, http.FileServerFS(u.static)))

	// The three admin sections each mount at a path of their own rather than
	// under one /admin parent, which is what lets the navbar tell them apart; see
	// registerNav. Each now takes the login guard itself, there being no shared
	// parent left to take it once. Which identities exist, and what groups each
	// is in, is the htpasswd file's answer for now.
	u.Route("/award", func(r webapp.Router) {
		r.Use(u.auth.RequireLogin())

		r.GetErr("/", u.uiViewAwardList)
		r.GetErr("/bulk-add", u.uiViewAwardBulkForm)
		r.PostErr("/bulk-add", u.uiViewAwardBulkSubmit)

		r.GetErr("/add", u.uiViewAwardFormSingle)
		r.PostErr("/add", u.uiViewAwardUpsert)

		r.GetErr("/{id}/edit", u.uiViewAwardFormSingle)
		r.PostErr("/{id}/edit", u.uiViewAwardUpsert)
		r.GetErr("/{id}/delete", u.uiViewAwardDeleteConfirm)
		r.PostErr("/{id}/delete", u.uiViewAwardDelete)
	})

	u.Route("/recipient", func(r webapp.Router) {
		r.Use(u.auth.RequireLogin())

		r.GetErr("/", u.uiViewRecipientList)
		r.GetErr("/bulk-add", u.uiViewRecipientBulkForm)
		r.PostErr("/bulk-add", u.uiViewRecipientBulkSubmit)

		r.GetErr("/add", u.uiViewRecipientFormSingle)
		r.PostErr("/add", u.uiViewRecipientUpsert)

		r.GetErr("/{id}/edit", u.uiViewRecipientFormSingle)
		r.PostErr("/{id}/edit", u.uiViewRecipientUpsert)
		r.GetErr("/{id}/delete", u.uiViewRecipientDeleteConfirm)
		r.PostErr("/{id}/delete", u.uiViewRecipientDelete)
	})

	u.Route("/winnings", func(r webapp.Router) {
		r.Use(u.auth.RequireLogin())

		r.GetErr("/", u.uiViewWinningList)
		r.GetErr("/assign", u.uiViewWinningAssignForm)
		r.PostErr("/assign", u.uiViewWinningAssignSubmit)
		r.GetErr("/clear-board", u.uiViewWinningClearBoard)

		// Present and reveal are GETs because they are remote-control presses, and
		// the only thing either writes is the mark reveal leaves on the row it names.
		// Putting a winning back is an edit with nothing to press, so unpresent is a
		// POST like the delete beside it.
		r.GetErr("/{id}/present", u.uiViewWinningPresent)
		r.GetErr("/{id}/reveal", u.uiViewWinningReveal)
		r.PostErr("/{id}/unpresent", u.uiViewWinningUnpresent)
		r.GetErr("/{id}/delete", u.uiViewWinningDeleteConfirm)
		r.PostErr("/{id}/delete", u.uiViewWinningDelete)
	})

	// What is left of /admin is the landing page a sign-in lands on and the three
	// sections' old addresses. Those redirect, so a bookmark or a page rendered
	// by the previous build still arrives instead of meeting a 404 the moment the
	// binary is replaced.
	u.Route(adminPrefix, func(r webapp.Router) {
		r.Use(u.auth.RequireLogin())

		r.GetErr("/", u.uiViewAdminLanding)

		// Only the sections that moved match, so a path that never existed under
		// /admin still comes back 404 rather than bouncing off to a guess. GET
		// only: a form half-filled on a page from before the move is refused when
		// it submits, and reloading the page is the whole remedy.
		r.GetErr("/award/*", u.uiRedirectMovedSection)
		r.GetErr("/recipient/*", u.uiRedirectMovedSection)
		r.GetErr("/winnings/*", u.uiRedirectMovedSection)
	})
}

// uiRedirectMovedSection sends an address from before the sections were broken
// out to the same place under the section's own top level path.
//
// The target is the old path with /admin removed and the result cleaned. The
// cleaning is not tidiness: a request for /admin/award//evil.com would otherwise
// hand out a Location that begins with two slashes, which a browser reads as a
// host rather than a path and which would take a signed-in producer off site.
func (u *ui) uiRedirectMovedSection(w http.ResponseWriter, r *http.Request) error {
	target := path.Clean("/" + strings.TrimPrefix(r.URL.Path, adminPrefix))
	// Clean drops a trailing slash, and each section's own page is mounted with
	// one, so handing back the cleaned path would cost the caller a second
	// redirect to arrive where they were already heading.
	if strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(target, "/") {
		target += "/"
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	u.SeeOther(w, r, target)
	return nil
}
