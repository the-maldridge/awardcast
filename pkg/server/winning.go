package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (u *ui) uiViewWinningList(w http.ResponseWriter, r *http.Request) error {
	winnings, err := winningsWithRelations(u.DB).Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing winnings: %w", err)
	}

	u.View(w, r, winningListTemplate, webapp.Context{"winnings": winnings})
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

	u.SeeOther(w, r, "/admin/winnings/")
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

	u.SeeOther(w, r, "/admin/winnings/")
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

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(winning); err != nil {
		return fmt.Errorf("encoding winning %d: %w", id, err)
	}
	return nil
}

// uiViewWinningPresent tells every presenter screen to load one winning. The
// route is a GET because it changes nothing on the server: the ceremony's state
// is what is on the screen, and this only says what to show next.
func (u *ui) uiViewWinningPresent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	return u.publish(winningMessage{Type: msgTypeLoad, WinID: id})
}

func (u *ui) uiViewWinningReveal(w http.ResponseWriter, r *http.Request) error {
	return u.publish(winningMessage{Type: msgTypeReveal})
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
