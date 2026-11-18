# AGENTS.md

AwardCast is a self-hosted award presentation app: one Go binary with an
authenticated admin UI for curating awards/recipients and an unauthenticated
public "presenter" page that animates the winner on screen. Admin actions are
relayed to the presenter over a WebSocket broadcast.

## Commands

| Task | Command |
| --- | --- |
| Build | `go build ./...` |
| Run dev server | `go run .` (listens on `:1323`) |
| Test | `go test ./...` — **no test files exist**; nothing to imitate, nothing fails |
| Vet | `go vet ./...` — currently reports a **pre-existing** `context.WithTimeout` leak at `main.go:49`, so a clean run is not a usable signal |
| Container | `docker build -t awardcast .` |

CGO is mandatory: the SQLite driver (`gorm.io/driver/sqlite` →
`mattn/go-sqlite3`) needs a C toolchain, and the Dockerfile installs
`sqlite-dev build-base` and links with `-ldflags '-extldflags "-static"'`
because the final image is `scratch` with `tini-static` as entrypoint.

## Running locally

```sh
# from the repo root, templates hot-swappable from disk
AWARDCAST_DEBUG=$PWD/pkg/server/theme go run .
```

Then open `http://localhost:1323/admin/` (redirects to `/login`), and open
`http://localhost:1323/public/present` in a second window as the display.
A checked-in `.htpasswd` at the repo root supplies credentials for local use
(`.htgroup` is empty); htpasswd/htgroup paths resolve relative to the process
CWD, so run from the repo root or set the env vars below.

### Environment

| Variable | Default | Notes |
| --- | --- | --- |
| `AWARD_ADDR` | `:1323` | listen address (`main.go`) |
| `AWARDCAST_DB` | `award.db` | SQLite path; Docker sets `/data/award.db`, so `/data` must be a volume |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error`, slog text on stderr |
| `AWARDCAST_DEBUG` | unset | load templates + static from disk instead of the embedded FS. **The value must be the theme directory** (`pkg/server/theme`), not the repo root: the code does `fs.Sub(tfs, "p2")` and `fs.Sub(tfs, "static")` on it |
| `AUTHWARE_BASIC_MECHS` | `htpasswd` | `:`-separated authware backends (`htpasswd`, `netauth`, `ldap` are importable; only htpasswd and netauth are imported in `main.go`) |
| `AUTHWARE_HTPASSWD_FILE` / `AUTHWARE_HTGROUP_FILE` | `.htpasswd` / `.htgroup` | from the authware htpasswd backend |
| `AUTHWARE_SESSION_KEY` | unset | **indirect**: this names *another* env var whose value is the cookie key. Unset ⇒ random key ⇒ all sessions die on restart |
| `AUTHWARE_SESSION_LIFETIME` | `1h` | Go duration |

Health check endpoint: `GET /-/alive` (chi heartbeat, no auth).

## Layout

```
main.go                 slog setup, signal-driven graceful shutdown (30s cap), Serve
pkg/server/server.go    Server struct, ALL route registration, template/auth/CSV helpers
pkg/server/award.go     admin CRUD handlers for awards
pkg/server/recipient.go admin CRUD handlers for recipients
pkg/server/winning.go   assign/list/delete winnings, presenter JSON, broadcast triggers
pkg/server/event.go     WebSocket fan-out (EventStream), refactored from coder/websocket chat example
pkg/types/type.go       GORM models: Award, Image, Recipient, Winning
pkg/server/theme/       embedded via go:embed; p2/ = pongo2 templates, static/ = css + vendored js
```

`New()` in `pkg/server/server.go` is the whole route table — every handler is
registered there and nowhere else. Schema is created by `d.AutoMigrate(...)` on
every startup; there is no migrations directory, so a model change is picked up
by restarting, and destructive changes require deleting the SQLite file.
`award.db` and `test_data/` are gitignored (the CSVs in `test_data/` are the
bulk-upload format: `Slug,Title,Subtitle` and `Name`).

## Data flow

Two halves share one process:

* `/admin/*` — cookie-authenticated, server-rendered CRUD against SQLite.
* `/public/*` — unauthenticated: `/public/e` (WebSocket subscriber),
  `/public/present` (self-contained display page),
  `/public/winnings/{id}/data` (JSON for one winning).

Presenting a winner:

1. `views/winning/list.p2` renders a `Present` button per row whose inline JS
   `fetch()`es `GET /admin/winnings/{id}/present`.
2. That handler (`uiViewWinningPresent`) writes nothing; it publishes
   `{"Type":1,"WinID":n}` to every WebSocket subscriber.
3. Each `/public/present` tab receives it, fetches the winning's JSON, and
   renders it through a bundled Mustache template.
4. `.../reveal` publishes `Type:2` (staged CSS reveal of the winner);
   `/admin/winnings/clear-board` publishes `Type:3` (clear stage).

Consequences worth remembering:

* The message type is a magic number duplicated in three places:
  `winningMessage.Type` in `pkg/server/winning.go:15`, the `MsgTypeLoad/Reveal/Clear`
  constants in `present.p2`, and the `fetch()` wiring in `views/winning/list.p2`.
  Changing the protocol means changing all three.
* The broadcast is fire-and-forget with no persistence or replay (buffer of 16
  per subscriber; slow clients are disconnected). A presenter reload shows an
  empty stage until the next Present.
* The presenter page hardcodes `ws://` + `document.location.host`, so it does
  not work behind TLS termination.

## Conventions

**Handlers.** Methods on `*Server` named `uiView*`. Read handlers end in
`doTemplate`; write handlers end in `http.Redirect(..., http.StatusSeeOther)`
back to the list page. Forms post to their own URL (`<form method="post">` with
no action), so a new route usually needs a `GET` and `POST` on the same path.

**Add vs. edit share one handler.** `GET/POST /admin/award/add` and
`GET/POST /admin/award/{id}/edit` both bind `uiViewAwardFormSingle` /
`uiViewAwardUpsert` (same for recipients). This works because `strToUint`
returns `0` for the absent `id` param and GORM's `Save` inserts when the primary
key is zero. Never route a *delete* through that idiom: an all-zero struct
condition makes GORM refuse with `WHERE conditions required`.

**Form field names are entity-prefixed**: `award_slug`, `award_title`,
`award_sub`, `recipient_name`, `awards_file`, `recipients_file`, and the select
values `award` / `recipient` for assigning a winning. Bulk upload CSV headers
become map keys verbatim (`csvToMap`).

**Templates.** Set root is `pkg/server/theme/p2`; names passed to `doTemplate`
are relative to it (`"views/award/list.p2"`). Child templates extend the base
with a path **relative to the including file** — `{% extends "../../base.p2" %}`
— so moving a template to a different depth means fixing the `../` count.
`base.p2` includes `fragments/nav.p2` and provides `title`/`content` blocks;
`login.p2` and `present.p2` are standalone full documents. `doTemplate` injects
`user` into every context from the authware request value, so templates (and
`nav.p2`'s login/logout link) can rely on it without handlers setting it.
`{% verbatim %}` is required around any JS that uses `{{ }}` (see `present.p2`)
so pongo2 does not consume it.

**Styles.** `static/css/theme.css` is a small hand-rolled utility set
(`flex-container`/`flex-row`/`flex-item`/`flex-max`, `box`, `button`,
`foreground`, `grid-center`, `noprint`) plus the `award-*` presenter animation
classes; compose those rather than adding new styles per page.

**Associations.** GORM needs explicit `Preload(clause.Associations)` before a
`Winning` is rendered; templates dereference `winning.Award.Slug` and
`winning.Recipient.Name`, and a missing Preload shows up as blank cells, not an
error.

**Deletes have no DB cascade.** Award/recipient delete handlers delete the child
`Winning` rows first, then the parent (`uiViewAwardDelete`,
`uiViewRecipientDelete`). Keep that order for any new relation.

## Gotchas

* **Templates are compiled once and cached.** `doTemplate` uses pongo2's
  `FromCache`, and the set's `Debug` flag is never enabled, so a template edit
  needs a process restart — even with `AWARDCAST_DEBUG` set, which only swaps
  the loader from embedded FS to disk.
* **The error templates referenced in code do not exist.** `errors/request.p2`
  and `views/errors/internal.p2` are used by error paths in all three CRUD files
  but are absent from `theme/p2`. Every error therefore renders pongo2's
  "Template ... could not be found" text, and the real error only appears in the
  slog output (warn level, so visible by default). Creating those two files is
  the fix if you touch error handling.
* **Error-path style is inconsistent.** award/recipient handlers mix
  `errors/request.p2` and `views/errors/internal.p2`, and several lookups use
  `Find` where `First` is intended (`uiViewRecipientFormSingle`,
  `uiViewWinningDeleteConfirm`), so a missing row is *not* an error there and
  renders an empty form/confirm page instead of a 404.
* **`go.mod` marks every requirement `// indirect`**, including chi, gorm,
  websocket and authware. `go mod tidy` reclassifies them, producing a large
  unrelated diff; gopls logs that warning continuously — it is not your change.
* Present and reveal endpoints are `GET`s with no side-effect guard; they are
  invoked by `fetch()` from the winnings list, not by navigation.
* Anything under `/public/` is intentionally unauthenticated, so award results
  are readable by anyone who can reach the port.
* `Image` (with the `award_graphics` join table) exists in the model and
  AutoMigrate list but has no handlers or UI yet.
