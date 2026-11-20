package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/the-maldridge/webapp"
	"github.com/the-maldridge/webapp/db"
	"gorm.io/gorm"

	"github.com/the-maldridge/awardcast/pkg/types"
)

const (
	winningListTemplate   = "@awardcast/views/winning/list.p2"
	winningFormTemplate   = "@awardcast/views/winning/form.p2"
	winningDeleteTemplate = "@awardcast/views/winning/delete.p2"

	// presentedParam is the winnings list filter, and its default is the point of
	// the whole thing: mid-ceremony the producer wants the awards that have not
	// gone out, and a wall of winners already shown pushes the next one down the
	// screen.
	presentedParam = "presented"

	// The three views the list offers: un-presented only, the reverse, and
	// everything. The value for the third spells itself out so a link says what it
	// shows rather than relying on an absent parameter.
	filterOpen      = "open"
	filterPresented = "presented"
	filterAll       = "all"
)

// The message types the presenter screen understands. They are a wire format
// shared with the JavaScript in present.p2, so the numbers there are these
// numbers.
const (
	msgTypeLoad   uint = 1
	msgTypeReveal uint = 2
	msgTypeClear  uint = 3
)

// winningMessage is what the presenter screen is told to do next. A reveal
// carries no id because it acts on whatever is already on the screen.
type winningMessage struct {
	Type  uint
	WinID uint
}

// listWinningFilter reads which of the three views was asked for. An absent filter
// is the default rather than everything: the list is a queue first.
//
// A value outside the three is bad input rather than a shrug. Falling back to the
// default would hand the producer a list they did not ask for, and the one thing
// not to be uncertain about during a ceremony is what a page is showing.
func listWinningFilter(r *http.Request) (string, error) {
	switch filter := r.URL.Query().Get(presentedParam); filter {
	case "", filterOpen:
		return filterOpen, nil
	case filterPresented, filterAll:
		return filter, nil
	default:
		return "", &webapp.BadInput{
			Name:   presentedParam,
			Value:  filter,
			Reason: "must be one of open, presented, all",
		}
	}
}

// uiViewWinningList shows the winnings a filter names, oldest first, which is
// assignment order and therefore roughly the order the ceremony runs.
//
// The two counts are not the list filtered twice. An empty list has to say whether
// nothing is assigned or everything has already gone out, and those want opposite
// buttons.
func (u *ui) uiViewWinningList(w http.ResponseWriter, r *http.Request) error {
	filter, err := listWinningFilter(r)
	if err != nil {
		return err
	}

	query := winningsWithRelations(u.DB).Order("id")

	// "is" and "is not" rather than "=" because the mark arrived with this version:
	// a database written before it has no value for presented, and AutoMigrate adds
	// the column without backfilling, so those rows read NULL. "presented = false"
	// is not true of a NULL, which would hide every award already assigned from both
	// filtered views the moment an existing deployment upgraded.
	switch filter {
	case filterOpen:
		query = query.Where("presented is not true")
	case filterPresented:
		query = query.Where("presented is true")
	}

	winnings, err := query.Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing winnings: %w", err)
	}

	presented, err := db.G[types.Winning](u.DB).
		Where("presented is true").
		Count(r.Context(), "id")
	if err != nil {
		return fmt.Errorf("counting presented winnings: %w", err)
	}
	total, err := db.G[types.Winning](u.DB).Count(r.Context(), "id")
	if err != nil {
		return fmt.Errorf("counting winnings: %w", err)
	}

	u.View(w, r, winningListTemplate, webapp.Context{
		"winnings":       winnings,
		"filter":         filter,
		"presentedCount": presented,
		"totalCount":     total,
	})
	return nil
}

// uiViewWinningAssignForm offers every award against every recipient. Both
// lists are needed to render one choice, so an empty either one is worth
// showing the producer as an empty picker rather than a form that cannot
// validate.
func (u *ui) uiViewWinningAssignForm(w http.ResponseWriter, r *http.Request) error {
	awards, err := db.G[types.Award](u.DB).Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing awards: %w", err)
	}
	recipients, err := db.G[types.Recipient](u.DB).Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing recipients: %w", err)
	}

	u.View(w, r, winningFormTemplate, webapp.Context{
		"awards":     awards,
		"recipients": recipients,
	})
	return nil
}

func (u *ui) uiViewWinningAssignSubmit(w http.ResponseWriter, r *http.Request) error {
	awardID, err := webapp.FormUint(r, "award")
	if err != nil {
		return err
	}
	recipientID, err := webapp.FormUint(r, "recipient")
	if err != nil {
		return err
	}

	winning := types.Winning{AwardID: uint(awardID), RecipientID: uint(recipientID)}
	if err := db.G[types.Winning](u.DB).Create(r.Context(), &winning); err != nil {
		return fmt.Errorf("assigning award %d to recipient %d: %w", awardID, recipientID, err)
	}

	u.SeeOther(w, r, "/winnings/")
	return nil
}

func (u *ui) uiViewWinningDeleteConfirm(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	winning, err := oneWinning(r.Context(), u.DB, id)
	if err != nil {
		return err
	}

	u.View(w, r, winningDeleteTemplate, webapp.Context{"winning": winning})
	return nil
}

func (u *ui) uiViewWinningDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}

	// The pairing goes away; the award and the recipient do not. A winning that
	// was on the presenter screen stays there until the board is cleared, which
	// is what it already did: deleting a mistake quietly from a screen the room
	// is watching would be the surprise.
	if _, err := db.G[types.Winning](u.DB).Where("id = ?", id).Delete(r.Context()); err != nil {
		return fmt.Errorf("deleting winning %d: %w", id, err)
	}

	u.SeeOther(w, r, "/winnings/")
	return nil
}

// uiViewWinningData is the one JSON endpoint in the application, and stays
// hand-written: the framework renders HTML through View and has no helper for a
// success body, and the presenter screen fetches this to paint an award.
func (u *ui) uiViewWinningData(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	winning, err := oneWinning(r.Context(), u.DB, id)
	if err != nil {
		return err
	}

	// The presenter paints colors from this response, so the award's theme
	// travels with it: the assigned theme when there is one, the default
	// otherwise. A miss at either step is no theme rather than a failed
	// request — a color lookup must not be what keeps the room from seeing
	// its winner.
	winning.Award.ColorTheme = u.themeForAward(r.Context(), winning.Award.ColorThemeID)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(winning); err != nil {
		return fmt.Errorf("encoding winning %d: %w", id, err)
	}
	return nil
}

// themeForAward resolves the theme one award wears: the theme its id names,
// and the default theme when the id is zero or dangles — dangling because a
// theme was deleted out from under the awards, which deleting a theme is
// allowed to do. Even the default can be missing on a database mid-bootstrap,
// and that answers no theme: the presenter's own fallbacks paint the old
// ivory screen, which is a worse-looking outcome than an error on the one
// screen the room is watching.
func (u *ui) themeForAward(ctx context.Context, id uint) *types.ColorTheme {
	if id != 0 {
		theme, err := db.G[types.ColorTheme](u.DB).Where("id = ?", id).First(ctx)
		switch {
		case err == nil:
			return &theme
		case !errors.Is(err, gorm.ErrRecordNotFound):
			slog.Warn("Could not load the color theme of an award; falling back to the default",
				"theme_id", id, "error", err)
		}
	}

	theme, err := db.G[types.ColorTheme](u.DB).Where("is_default is true").First(ctx)
	switch {
	case err == nil:
		return &theme
	case !errors.Is(err, gorm.ErrRecordNotFound):
		slog.Warn("Could not load the default color theme; painting without one", "error", err)
	}
	return nil
}

// uiViewWinningPresent tells every presenter screen to load one winning. The
// route is a GET because it changes nothing on the server: the ceremony's state is
// what is on the screen, and this only says what to show next. Its sibling below
// is where a winning is marked as having gone out, because loading an award in
// front of an empty room is not presenting a winner.
func (u *ui) uiViewWinningPresent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	return u.publish(winningMessage{Type: msgTypeLoad, WinID: id})
}

// uiViewWinningReveal is where a winning becomes presented: the award is already
// on the screen from present, and this names the winner to the room.
//
// The mark uses the id in the path even though the message carries none, since a
// reveal acts on whatever a screen happens to be holding. Whoever reveals a row
// other than the one showing has marked the wrong winning, which is the ambiguity
// this route already had; the mark only adds that the row then leaves the queue.
//
// Marking runs before publishing so that a request for a winning which does not
// exist cannot move the projector, and the write sets one column to a fixed value:
// a second reveal, or a browser prefetching the link, cannot mark anything the
// click did not mean.
func (u *ui) uiViewWinningReveal(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := u.markPresented(r, id, true); err != nil {
		return err
	}
	return u.publish(winningMessage{Type: msgTypeReveal})
}

// uiViewWinningUnpresent puts a winning back in the queue after a mis-click or a
// reveal that never reached the projector.
//
// It is a POST carrying a CSRF token, unlike the two routes above: those are GETs
// because pressing a remote control is not an edit, while this one edits a row and
// moves nothing on the screen.
//
// The redirect names the all view rather than the default one, because the row
// just acted on would otherwise disappear from the page the redirect lands on,
// which reads exactly like the action having failed the other way round.
func (u *ui) uiViewWinningUnpresent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := u.markPresented(r, id, false); err != nil {
		return err
	}
	u.SeeOther(w, r, "/winnings/?"+presentedParam+"="+filterAll)
	return nil
}

// markPresented sets or clears the mark on one winning. Zero rows updated is a
// winning that is not there rather than a successful write of nothing, which is
// the difference between a 404 and a click that lied about working.
func (u *ui) markPresented(r *http.Request, id uint, presented bool) error {
	rows, err := db.G[types.Winning](u.DB).
		Where("id = ?", id).
		Update(r.Context(), "presented", presented)
	if err != nil {
		return fmt.Errorf("marking winning %d presented=%t: %w", id, presented, err)
	}
	if rows == 0 {
		return fmt.Errorf("winning %d: %w", id, webapp.ErrNotFound)
	}
	return nil
}

func (u *ui) uiViewWinningClearBoard(w http.ResponseWriter, r *http.Request) error {
	return u.publish(winningMessage{Type: msgTypeClear})
}

// publish hands a message to the event stream, which never blocks: a presenter
// screen too slow to keep up is closed rather than allowed to hold up the
// screens that are keeping up.
func (u *ui) publish(msg winningMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encoding %+v: %w", msg, err)
	}
	u.stream.publish(body)
	return nil
}

// winningsWithRelations loads a winning with both sides of the pairing named
// out. The generic query API has no preload-everything shorthand, and the list,
// the delete confirmation, and the presenter all need the award and the
// recipient rather than two bare foreign keys.
func winningsWithRelations(d *db.DB) gorm.ChainInterface[types.Winning] {
	return db.G[types.Winning](d).Preload("Award", nil).Preload("Recipient", nil)
}

// oneWinning loads one winning with its award and recipient, turning a missing
// row into the framework's not-found.
func oneWinning(ctx context.Context, d *db.DB, id uint) (types.Winning, error) {
	winning, err := winningsWithRelations(d).
		Where("id = ?", id).
		First(ctx)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return types.Winning{}, fmt.Errorf("winning %d: %w", id, webapp.ErrNotFound)
	case err != nil:
		return types.Winning{}, fmt.Errorf("loading winning %d: %w", id, err)
	}
	return winning, nil
}
