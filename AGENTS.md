# AGENTS.md

AwardCast is a self-hosted award presentation app: one Go binary with an
authenticated admin UI for curating awards and recipients, and an
unauthenticated public "presenter" page that animates the winner on screen.
Admin actions reach the presenter over a WebSocket broadcast.

It is an application built on `github.com/the-maldridge/webapp`: the framework
owns configuration, the database handle, routing, rendering, the error funnel,
sessions, CSRF, and the Bulma layout. AwardCast owns the models, the handlers,
the templates under its own namespace, and the event stream.

## Commands

| Task | Command |
| --- | --- |
| Build | `CGO_ENABLED=1 go build ./...` |
| Run dev server | `AUTHWARE_SESSION_KEY=devkey go run .` (listens on `:1323`) |
| Test | `CGO_ENABLED=1 go test ./...` |
| Presenter check | `node scripts/presenter-check.mjs --url http://127.0.0.1:1323/public/present` — headless Chromium over CDP; needs the server running |
| Format | `gofmt -l .` — must print nothing |
| Vet | `go vet ./...` — a clean run is the expectation, and it holds |
| Container | `docker build -t awardcast .` |

`CGO_ENABLED=1` is required for build and test alike: the SQLite driver
(`gorm.io/driver/sqlite` → `mattn/go-sqlite3`) is C. The Dockerfile installs
`sqlite-dev build-base` and links with `-ldflags '-extldflags "-static"'`
because the final image is `scratch` with `tini-static` as entrypoint.

`go mod tidy` is a normal command again — `go.mod` lists its direct requires
correctly, so tidying produces no churn.

## Running locally

```sh
# from the repo root, so .htpasswd and .htgroup resolve
AUTHWARE_SESSION_KEY=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n') go run .
```

Open `http://localhost:1323/admin/` for the admin UI (it answers 401 until you
sign in at `/login`), and `http://localhost:1323/public/present` in a second
window or on the projector.

`AWARDCAST_THEME_DIR` reads the page templates from a directory instead of the
copies compiled into the binary:

```sh
AWARDCAST_THEME_DIR=$PWD/pkg/server/theme/p2 AUTHWARE_SESSION_KEY=devkey go run .
```

The value is the **`p2` directory**, not `pkg/server/theme` and not the repo
root. Static assets are always served from the embedded copy; only pages are
overlaid. Templates are still compiled once at startup, so an edit needs a
restart — this saves the Go toolchain on a deployment host, not the restart.

The framework's own `WithTemplateDebug` would recompile per request, but it
mounts the directory under a `dev` namespace, and a page name qualified
`@awardcast/…` never resolves there. So it is not wired up, and
`AWARDCAST_THEME_DIR` is a startup-time overlay only.

The old name for this was `AWARDCAST_DEBUG`. That name now belongs to the
framework, which parses it as a boolean and refuses to start when it is set
alongside `AWARDCAST_PRODUCTION`. Do not put a path in it.

### Environment

| Variable | Default | Notes |
| --- | --- | --- |
| `AWARDCAST_ADDR` | `:1323` | listen address; the application only supplies this default when the variable is unset, so the environment wins |
| `AWARDCAST_DB` | `webapp.db` | SQLite path; the Dockerfile sets `/data/award.db`, so `/data` must be a volume |
| `AWARDCAST_THEME_DIR` | unset | serve page templates from this directory instead of the embedded copy |
| `AWARDCAST_READ_TIMEOUT` / `AWARDCAST_WRITE_TIMEOUT` | `24h` | Go durations, read by `timeouts()` in `server.go`; a value that does not parse is a startup error naming the variable. See the socket note under **Data flow** |
| `AWARDCAST_PRODUCTION` | unset | framework hardening: Secure cookies, no template debugging |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`, slog text on stderr (the one variable `main.go` reads itself) |
| `AUTHWARE_SESSION_KEY` | unset | long random string. Unset means a random key per boot, so every restart signs everybody out |
| `AUTHWARE_BASIC_MECHS` | `htpasswd` | `:`-separated backends; `htpasswd` and `netauth` are the two compiled in |
| `AUTHWARE_HTPASSWD_FILE` / `AUTHWARE_HTGROUP_FILE` | `.htpasswd` / `.htgroup` | resolved against the process CWD, and **both must exist** (`.htgroup` may be empty) |
| `AUTHWARE_SESSION_LIFETIME` | `1h` | Go duration |

Two knobs that used to be listed here are gone because nothing ever read them:
`AWARDCAST_IDLE_TIMEOUT`, since the application passes its timeouts explicitly
and the framework fills none of them from the environment, and
`AWARDCAST_MAX_BODY_BYTES`, since the 16MiB cap is set unconditionally in
`Options()` — bulk import is a feature, not a tunable.

Health check: `GET /-/alive`, served by the framework without touching the
database. The image is `scratch`, so there is no `HEALTHCHECK` instruction in
the Dockerfile — probe it from the orchestrator instead.

## Layout

```
main.go                 slog setup, then webapp's Run (it owns signal handling)
scripts/                presenter-check.mjs: the reveal choreography over CDP, against a running server
pkg/server/server.go    options, Install, the whole route table, nav, auth wiring
pkg/server/award.go     award CRUD and CSV import
pkg/server/colortheme.go color theme CRUD, gradient/contrast validation, the seeded default
pkg/server/recipient.go recipient CRUD and CSV import
pkg/server/winning.go   assign/list/delete winnings, presenter JSON, broadcast triggers
pkg/server/pages.go     login, presenter, admin landing
pkg/server/params.go    path and form id parsing
pkg/server/csv.go       CSV reading, uploaded-file extraction
pkg/server/event.go     WebSocket fan-out (EventStream)
pkg/types/type.go       GORM models: Award, ColorTheme, Image, Recipient, Winning
pkg/server/theme/       embedded via go:embed
  p2/                   page templates (present.p2, views/…) — no base, no nav, no login
  static/               present.css, mustache.js, reconnecting-websocket.js
```

`registerRoutes` in `pkg/server/server.go` is the whole route table, and it is
the only place a public path is named. Schema comes from `db.WithAutoMigrate`
in `Options()`; there is no migrations directory, so a model change lands on
restart and a destructive one means deleting the SQLite file. `award.db` and
`test_data/` are gitignored; the CSVs in `test_data/` are the bulk-upload
format (`Slug,Title,Subtitle` and `Name`).

### Building an application: New, Options, Install

Templates can only be mounted while an application is constructed, but routes,
nav, and authentication all need a constructed application to attach to. The
split is therefore:

- `Options()` — templates, models, body cap. Safe to hand to any `webapp.New`.
- `runtimeOptions()` (private) — env prefix, address, timeouts.
- `Install(app)` — identity service, nav, routes, static files.
- `New()` — all of the above, which is what `main.go` calls.

Tests go through `webapptest.NewApp(t, Options()...)`, then
`app.InstallAuth()`, then `Install(app.App)`. `Install` keeps an identity
service the application already has, which is what lets a test use the
harness's own account store instead of the `.htpasswd` file in whatever
directory the tests run from.

## Data flow

Two halves share one process:

* `/award/*`, `/colors/*`, `/recipient/*`, `/winnings/*` — the admin CRUD,
  server-rendered over SQLite and behind a signed-in identity, each section
  owning one path segment (see the navbar gotcha below). `/admin/` is the landing page a sign-in
  lands on, and `/admin/{section}/*` redirects to the section's own address for
  bookmarks and pages rendered before the sections were broken out.
* `/public/*` — unauthenticated: `/public/e` (WebSocket), `/public/present`
  (standalone display page), `/public/winnings/{id}/data` (JSON for one
  winning, its award's resolved `ColorTheme` riding along).

Presenting a winner:

1. `views/winning/list.p2` renders Present/Reveal buttons carrying
   `data-action`; one delegated listener fetches that URL.
2. Present writes nothing — it publishes `{"Type":1,"WinID":n}` to every
   WebSocket subscriber.
3. Each `/public/present` tab receives it, fetches the winning's JSON, and
   renders it through a Mustache template held in the page.
4. `.../reveal` marks that winning **presented** and publishes `Type:2` (staged
   CSS reveal); `/winnings/clear-board` publishes `Type:3` (clear stage).

Presented is what the winnings list filters on, and the default is the queue
rather than the roster: `?presented=open` (or no parameter) is what has not gone
out, `?presented=presented` is what has, `?presented=all` is everything. The
reveal sets the mark rather than the present, because loading an award in front of
an empty room has not shown anybody a winner, and `POST /winnings/{id}/unpresent`
is the undo for a reveal that never reached the projector.

Worth knowing:

* The message type is defined once. `msgTypeLoad/Reveal/Clear` in
  `pkg/server/winning.go` are rendered into `present.p2` when the page is
  served, so the page carries the server's own numbers and there is no second
  copy to drift. `TestPresenterCarriesTheWireFormat` is what fails if the
  render ever stops putting them there.
* The broadcast is fire-and-forget with no replay (16-message buffer per
  subscriber, slow clients are dropped). A presenter reload shows an empty
  stage until the next Present.
* The socket URL is chosen from the page's protocol, so `wss://` works behind
  TLS termination. It used to hard-code `ws://`.
* **The read and write timeouts default to 24 hours.** Accepting a WebSocket
  hijacks the connection and a hijacked connection keeps the deadlines the
  server set beforehand; `coder/websocket` never clears them. Under the
  framework's 30-second default the presenter would drop its socket twice a
  minute and miss whatever was clicked in between. Lower it per deployment
  with `AWARDCAST_READ_TIMEOUT`/`_WRITE_TIMEOUT` only if there is no socket to
  keep alive.

## Conventions

**Handlers.** Methods on `*ui` named `uiView*`, matching
`webapp.HandlerErr` (`func(http.ResponseWriter, *http.Request) error`). Never
write a status and then call `View`: return an error and let the funnel pick
the status, the log level, and whether the response is a re-render or an error
page. Redirects are `u.SeeOther(w, r, path)`, which is a 303 to the list page.

**Errors are typed, not numbered.** `webapp.ErrNotFound` renders 404, a
`*webapp.BadInput` renders 400, `*http.MaxBytesError` renders 413, and
`*webapp.Rejected{Template, Values, Reasons, Status}` re-renders a form with
what was typed plus why it was refused (422 unless another status is set). A
lookup helper that can miss does:

```go
award, err := oneAward(ctx, u.DB, id)   // maps gorm.ErrRecordNotFound to webapp.ErrNotFound
```

**Add and edit share one handler.** `GET/POST /award/add` and
`GET/POST /award/{id}/edit` both bind `uiViewAwardFormSingle` and
`uiViewAwardUpsert`, and `formID(r)` is what tells them apart: no `{id}` in the
path means 0 (insert), a malformed one is a 400 rather than a silent zero.
Writes go through `db.InsertOrUpdate(ctx, u.DB, &award,
db.OnConflictColumns("id"))`. Never route a delete through that idiom.

**Form field names are entity-prefixed**: `award_slug`, `award_title`,
`award_sub`, `recipient_name`, `awards_file`, `recipients_file`, and `award` /
`recipient` for assigning a winning. Read them with `webapp.FormString` /
`webapp.FormUint`, never `r.FormValue` — the typed helpers are what turn a bad
field into a 400.

**CSV headers become map keys verbatim and are case-sensitive** (`Slug`,
`Title`, `Subtitle`, `Name`). A file whose rows disagree with its own header
length is refused whole with the row number; a file that parses but holds a
duplicate slug imports the rows it could place and lists the ones it could
not. That asymmetry is deliberate: a mis-shaped file means the producer is
holding the wrong export, while one repeated slug at row 400 of 900 should not
discard 899 awards at 9pm.

**Templates.** Names are namespaced. Every page is
`@awardcast/views/…` and extends `@core/base.p2`, which offers exactly six
blocks: `title`, `head`, `bodyclass`, `header`, `content`, `footer`. There is
no `base.p2` or `fragments/nav.p2` in this repository any more — the theme owns
the layout and the navbar, and a page that wants something outside those six
blocks is wrong.

* `{% extends "@core/base.p2" %}` — never a `../` path. Relative extends are
  gone.
* `{{ csrfInput }}` goes inside **every** POST form, including the delete
  confirmations and the file uploads. A form without it is refused with 403
  once the browser holds a session.
* `{% if rejected %}…{% for reason in rejected %}` is the notification hook;
  `rejected` is a `[]string` the funnel sets when a handler returns
  `*webapp.Rejected`.
* `{% verbatim %}` is required around JS that uses `{{ }}` (see `present.p2`).
* Single-line `{# … #}` comments cannot span lines — use
  `{% comment %}…{% endcomment %}`.
* The presenter (`present.p2`) is a standalone document that extends nothing,
  because the layout would put a navbar and a sign-out button on the screen the
  room is watching. It loads `/static/awards/present.css`.

**Static assets** are served under `/static/awards/…` from the embedded FS.
`/static/core` belongs to the framework's theme (Bulma, navbar.js); do not add
files there. The admin pages use Bulma classes (`box`, `table is-fullwidth
is-striped`, `field`/`label`/`control`, `button is-primary`,
`notification is-danger is-light`); `present.css` carries only the `award-*`
animation rules, so a projector repaint cannot be caused by an admin markup
change.

**Associations need explicit Preloads.** The generics API has no
preload-everything shorthand — `Preload(clause.Associations)` fails with
`relation * not found` — so `winningsWithRelations` names both sides:
`db.G[types.Winning](d).Preload("Award", nil).Preload("Recipient", nil)`. The
`query` argument is not variadic; pass `nil`. A missing Preload shows up as
blank cells, not an error.

**Lists carry their own `ORDER BY`.** An unORDERed `SELECT` returns rows in
whatever order the engine likes, and insertion order is a habit SQLite keeps
only until it decides otherwise. Awards order by `slug`, recipients by `name`,
winnings by `id`; all three are unique, so the order is total.
`TestListsArriveInTheirNamedOrder` pins the first two.

**Deletes have no DB cascade.** Award and recipient deletes remove the child
`Winning` rows first, then the parent, inside `db.Transaction`. Keep that order
for any new relation. Inside the transaction the handle is a `*gorm.DB`, so use
`gorm.G[T](tx)`, not `db.G[T]`.

## Access control

Each admin section — `/award`, `/colors`, `/recipient`, `/winnings`, and what
is left of `/admin` — mounts behind its own `r.Use(u.auth.RequireLogin())`. That is an
identity check, not a permission check. Permission gating would deny everyone
today: authware grants
are satisfied by group names, and this repository's `.htgroup` is empty. Only
add a `Require(perm)` once the group file says who has what.

The session cookie is deliberately re-pinned to authware's plain `session`
name after `InstallAuth`, which would otherwise name it after the environment
prefix. That is what keeps an upgrade from signing out whoever was already
signed in.

`/logout` is **POST only** (the theme's navbar posts to it with a CSRF token).
The `GET /logout` link of the previous version is gone; a prefetching crawler
should not be able to sign people out.

## Testing

`pkg/server/server_test.go` drives the real route table through
`webapp/webapptest` against a temporary SQLite file. Use `newTestApp(t)` for
the anonymous case and `signedInApp(t)` for everything else.
`pkg/server/templates_test.go` is separate because it tests no handler: it
mounts the embedded pages and the selected theme and compiles every template
before any request is made.

Three things about the harness cost time to rediscover:

* `signedInApp` does a `GET /admin/` on the way in. A CSRF token exists only
  after a response has offered one, and a session minted straight into the
  cookie jar has never seen a page — so without that GET every write comes
  back 403, which is the middleware working and reads exactly like it failing.
* `a.Account(id, pw)` creates a **local password** account. The application
  installs `WithOnlyMechanisms(htpasswd)`, so a local account cannot
  authenticate against it. Use `a.SignIn("someone")`, which mints a session
  directly.
* `testas.As(req, id, groups…)` stamps a context identity that cannot satisfy
  a route guard. It is for a handler tested in isolation, not for a route test.

`themetest` does not apply here: this is an application using a theme, not a
theme, and the suite has no layout of its own to check.

## Gotchas

* The presented filter is spelled `presented is not true` and `presented is true`,
  never `= false`. AutoMigrate adds a column and does not fill it in, so a winning
  written before the mark existed reads NULL, and `presented = false` is not true of
  a NULL: every award an existing deployment had already assigned would have
  vanished from both filtered views on upgrade. `TestLegacyWinningsStayInTheQueue`
  blanks the column to keep that from being rediscovered mid-ceremony.
* `GET /winnings/{id}/reveal` writes a row. It is the only GET that does: the mark
  is one column set to a fixed value, so a second reveal or a prefetched link marks
  what the click meant and nothing more, and the button stays shaped like the rest
  of the remote control. Anything else that edits a row is a POST with a token.
* The navbar lights an item by comparing the **first path segment** of its link
  with the first segment of the request, which is why the four admin menus are
  `Awards`/`Colors`/`Recipients`/`Winnings` at `/award`, `/colors`,
  `/recipient`, `/winnings` rather than children of one `/admin` page: mount two
  menus under one prefix and both light up on every page. All four share
  `sectionWeight`, ties break alphabetically, and that is the order a producer
  sees. The one cost is that `/admin/` lights nothing, because it belongs to no
  section. Both facts are pinned by `TestNavSplitsIntoSectionMenus` and
  `TestLandingPageLightsNoMenu`.
* Present and reveal are `GET`s with no side-effect guard: they change nothing
  server-side, they publish an event. Anyone who can reach the admin UI can
  make the projector move; anyone who can reach `/public/*` can already see the
  results.
* Anything under `/public/` is unauthenticated, so award results are readable
  by anyone who can reach the port.
* Templates are compiled on first render and then cached, whether they came
  from the embedded copy or from `AWARDCAST_THEME_DIR`. A template edit needs a
  restart, and a template with a syntax error surfaces as a 500 on that page
  rather than as a startup failure — the framework only compiles its own
  required theme templates at boot. `TestEveryTemplateCompiles` is the backstop:
  it rebuilds the mount and parses every page in the embedded copy, so a broken
  template is a test failure rather than a ceremony outage. It checks the
  embedded copy only; an override under `AWARDCAST_THEME_DIR` is on the operator.
* `Image` (with the `award_graphics` join table) exists in the model and the
  migrate list but has no handlers or UI yet. Its table is registered so a
  database written by an older version is never rewritten out from under
  somebody.
* The CSRF middleware exempts an unsafe request that carries neither a session
  nor a CSRF cookie. That is what lets the very first anonymous `POST /login`
  through without a token; it is not a hole to rely on elsewhere.
* The presenter's reveal is three additive classes whose timing comes entirely
  from transitions, and **every transition lives on `#winner`, never on a stage
  class**. `transition` is a shorthand, so a stage rule naming only the opacity it
  is about replaces whatever the previous stage started; cancelling the height
  growth mid-flight jerks the whole board upward, because the growing block is
  what carries the title. An id selector also outranks a class, which is the
  second line of defence.
* The name fades in when the award stops moving, not on a timer: `revealWinner`
  awaits `transitionend` for the title's two font-size transitions and the block's
  `max-height`, and the fallback it races them against is read off
  `getComputedStyle`, so retiming any step is a stylesheet edit alone. That only
  works because the growth is aimed at the block's **measured** height rather than
  the `1000px` in stage2, so the transition ending is the same moment the motion
  ends. Leave those two facts coupled.
* The recipient's name is sized to fit two lines by a binary search on
  `getComputedStyle(name).fontSize`, counting line boxes with
  `Range.getClientRects()`, and it is re-run on resize. Two traps: line boxes are
  counted rather than the string measured, because the wrap depends on font
  metrics the projector and the laptop disagree about; and the measurement must
  happen at the width the reveal will have, which is why `html, body` set
  `overflow: hidden` — without it, opening the clipped block to measure it grows
  the document, a scrollbar appears, the layout width drops 15px, and the name
  comes out a line short. The `#award-container` `width` is load bearing for the
  same family of reason: as a fit-content grid item its min-content is one
  unbreakable name, which ran 4204px across a 1920px screen. Its value is `80%`
  rather than `100%` for presentation reasons, because type running edge to edge
  does not read as centred on a projection, so do not widen it back for the extra
  room: the wrap width the name is fitted against is 80% of the window, and that
  is what sets how narrow a window the two-line guarantee survives. For the worst
  name tried, a 53-character one, it holds down to a ~1220px window; below that
  `minNameFloor` binds before the second line is reached and a third line shows.
* All of the above is checkable without a room full of people, and
  `scripts/presenter-check.mjs` does it: point it at a running server, and it
  drives `/public/present` in headless Chromium over CDP, paints a synthetic
  award through the page's own template, calls `revealWinner()`, and samples the
  block every frame. It asserts the height is monotonic with no jump over 30px,
  that opacity is still zero when the height stops changing, that line count
  never exceeds two, and that the block measures 80% of `window.innerWidth` and
  never crosses an edge of it. It exits nonzero with the failing measurement
  named, and `--width` is how the ~1220px two-line floor is demonstrated rather
  than remembered.

* A **ColorTheme is not the theme**. Capital-T Theme is the compiled-in webapp
  layout (`webapp.WithTheme("bulma")`, the `@core` templates, `/static/core`);
  a ColorTheme is the stage colors a venue types into a form at
  `/colors/add`, and the `webapp-bulma` replace directive in `go.mod` belongs
  to the first meaning while the word in `/colors` belongs to the second. Keep
  them separate in names and in prose.
* Stage colors ride on custom properties the script sets on the body
  (`--award-bg`, `--award-bg-image`, `--award-text`, `--award-winner`), and
  **every paint clears all four before setting any** — without the clear, the
  previous award's gradient keeps painting behind an award that asked for a
  solid background. A gradient is one property: the script composes the whole
  `linear-gradient()` from colors that each passed its own hex check, so the
  stop count never needs a CSS switch — a solid theme sets no image and the
  stylesheet's `none` leaves the plain `--award-bg` color showing. Color
  transitions belong to `.award-bg` only; no stage class may transition a
  color, which is the same shorthand trap the reveal timing bullet above
  describes.
* A background is **up to five colors** (`Bg` then `BgTo`, `Bg3`…`Bg5`) that
  fill in order with no gaps; the count of non-empty colors is the whole
  gradient story, and the form's solid checkbox clears all the extras at
  once. The contrast check for a gradient is not just every stop at 4.5
  against the text: it is also **no text luminance strictly between the
  dimmest and brightest stop** — luminance runs monotonically between adjacent
  stops, so that one bracket rule is the full crossing rule for any stop
  count. Black-white-black behind a mid-gray title clears every per-stop
  ratio and still loses the title twice.
  `TestColorThemesValidateTheirColors` and
  `TestColorThemeGradientsCarryUpToFiveColors` hold the straddles that prove
  a naive check waves them through.
* **`AllowIllegible` is a waiver, not a validation mode.** A sponsor scheme
  that must be exact may be saved failing the contrast checks when the venue
  checks the box — readability is the venue's to trade away, and the list
  marks such a theme with `color-contrast-off-tag` so nobody forgets. It
  skips `contrastReasons` and nothing else: hexes, directions, in-order
  fills, and names are still refused when wrong, because a color cannot be
  compared for contrast without first being a color.
* There is exactly one default ColorTheme, enforced in the one writer
  (`uiViewColorThemeUpsert` clears the other rows' flags in the same
  transaction as the save) and defended at both ends: un-checking the current
  default and deleting the default are both refusals, because a missing
  default means the unassigned awards have nothing to fall back to. Deleting
  a non-default theme is fine even while awards wear it — the dangling id
  resolves to the default at read time (`themeForAward`), which is also what
  `Award.ColorThemeID = 0` means.
