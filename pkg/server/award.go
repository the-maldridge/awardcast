package server

import (
	"context"
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
	awardListTemplate    = "@awardcast/views/award/list.p2"
	awardFormTemplate    = "@awardcast/views/award/single.p2"
	awardDeleteTemplate  = "@awardcast/views/award/delete.p2"
	awardBulkAddTemplate = "@awardcast/views/award/bulk-add.p2"

	awardsFileField = "awards_file"

	// maxReportedBulkFailures bounds how many of an import's failures are
	// listed. A file that fails entirely produces one reason per row otherwise,
	// and a page full of repeated database errors says nothing a count does.
	maxReportedBulkFailures = 10

	// bulkFileReason explains a submit that carried no file at all. The form
	// marks the input required, so this is a dropped file or a hand-built
	// request.
	bulkFileReason = "No file was uploaded. Choose a CSV file and try again."
)

// awardListRow is one award plus the theme it will actually wear on the
// projector: its own when it has one, the default when the id is zero or
// dangles. Resolving before rendering keeps the fallback rule in Go, next to
// the identical rule the presenter's JSON follows, rather than restated in
// template.
type awardListRow struct {
	types.Award

	Theme types.ColorTheme
}

func (u *ui) uiViewAwardList(w http.ResponseWriter, r *http.Request) error {
	// Ordered by slug because the table leads with slug and an unORDERed SELECT
	// only happens to come back in insertion order. Slug is unique, so the order
	// is total and the list is the same for every viewer of the same data.
	awards, err := db.G[types.Award](u.DB).Order("slug").Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing awards: %w", err)
	}

	themes, err := db.G[types.ColorTheme](u.DB).Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing color themes for the award list: %w", err)
	}
	byID := make(map[uint]types.ColorTheme, len(themes))
	var fallback *types.ColorTheme
	for i := range themes {
		byID[themes[i].ID] = themes[i]
		if themes[i].IsDefault {
			fallback = &themes[i]
		}
	}

	rows := make([]awardListRow, 0, len(awards))
	for _, award := range awards {
		row := awardListRow{Award: award}
		if theme, ok := byID[award.ColorThemeID]; ok {
			row.Theme = theme
		} else if fallback != nil {
			row.Theme = *fallback
		}
		rows = append(rows, row)
	}

	u.View(w, r, awardListTemplate, webapp.Context{"awards": rows})
	return nil
}

// uiViewAwardFormSingle serves the add form and the edit form. With no id in
// the path there is nothing to load and the form is blank; with one, a missing
// award is a 404 rather than an empty form that would create a new award when
// submitted.
func (u *ui) uiViewAwardFormSingle(w http.ResponseWriter, r *http.Request) error {
	id, err := formID(r)
	if err != nil {
		return err
	}

	award := types.Award{}
	if id != 0 {
		if award, err = oneAward(r.Context(), u.DB, id); err != nil {
			return err
		}
	}

	themes, err := colorThemeChoices(r.Context(), u.DB)
	if err != nil {
		return err
	}

	u.View(w, r, awardFormTemplate, webapp.Context{
		"award":  award,
		"themes": themes,
	})
	return nil
}

// colorThemeChoices lists the themes an award form offers, named the way the
// producer named them.
func colorThemeChoices(ctx context.Context, d *db.DB) ([]types.ColorTheme, error) {
	themes, err := db.G[types.ColorTheme](d).Order("name").Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing color themes: %w", err)
	}
	return themes, nil
}

// uiViewAwardUpsert writes an award from either form. A zero id inserts and a
// non-zero one updates, and the upsert says so outright: the Save this replaced
// could not tell "new award" from "the path said abc", and created a row for
// both.
func (u *ui) uiViewAwardUpsert(w http.ResponseWriter, r *http.Request) error {
	id, err := formID(r)
	if err != nil {
		return err
	}

	slug, err := webapp.FormString(r, "award_slug")
	if err != nil {
		return err
	}
	title, err := webapp.FormString(r, "award_title")
	if err != nil {
		return err
	}
	sub, err := webapp.FormString(r, "award_sub")
	if err != nil {
		return err
	}
	themeID, err := formOptionalUint(r, "award_theme")
	if err != nil {
		return err
	}

	award := types.Award{ID: id, Slug: slug, DispTitle: title, DispSub: sub, ColorThemeID: themeID}

	// A theme id that is not in the list is a rejection rather than a save
	// that quietly drops it: the producer chose a theme, and writing an
	// award that will wear the default instead is the kind of surprise this
	// application has no room for. The form is also what held the stale
	// choice, so the form is where the answer goes.
	themes, err := colorThemeChoices(r.Context(), u.DB)
	if err != nil {
		return err
	}
	if themeID != 0 {
		if _, err := oneColorTheme(r.Context(), u.DB, themeID); err != nil {
			if errors.Is(err, webapp.ErrNotFound) {
				return &webapp.Rejected{
					Template: awardFormTemplate,
					Values:   webapp.Context{"award": award, "themes": themes},
					Reasons: []string{
						"That color theme is no longer in the list. Pick another, or leave the award on the default.",
					},
				}
			}
			return err
		}
	}

	if err := db.InsertOrUpdate(r.Context(), u.DB, &award, db.OnConflictColumns("id")); err != nil {
		return fmt.Errorf("saving award %q: %w", slug, err)
	}

	u.SeeOther(w, r, "/award/")
	return nil
}

func (u *ui) uiViewAwardDeleteConfirm(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	award, err := oneAward(r.Context(), u.DB, id)
	if err != nil {
		return err
	}

	u.View(w, r, awardDeleteTemplate, webapp.Context{"award": award})
	return nil
}

func (u *ui) uiViewAwardDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}

	// The winnings naming this award go first, in the same transaction as the
	// award itself: a winning left behind would render a line with no award on
	// the screen the room is watching, and the two statements could not fail
	// halfway before this application ran on a database that wraps writes for
	// you.
	err = db.Transaction(r.Context(), u.DB, func(tx *gorm.DB) error {
		if _, err := gorm.G[types.Winning](tx).Where("award_id = ?", id).Delete(r.Context()); err != nil {
			return fmt.Errorf("deleting the winnings for award %d: %w", id, err)
		}
		if _, err := gorm.G[types.Award](tx).Where("id = ?", id).Delete(r.Context()); err != nil {
			return fmt.Errorf("deleting award %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	u.SeeOther(w, r, "/award/")
	return nil
}

func (u *ui) uiViewAwardBulkForm(w http.ResponseWriter, r *http.Request) error {
	u.View(w, r, awardBulkAddTemplate, nil)
	return nil
}

// uiViewAwardBulkSubmit imports an awards CSV. A file that cannot be read comes
// back whole as a rejection, so the producer learns which row was wrong. A file
// that reads cleanly but holds a duplicate slug imports the rows it can and
// reports the ones it could not place: discarding an entire ceremony's awards
// because one line repeated an existing slug is the worse failure at 9pm.
func (u *ui) uiViewAwardBulkSubmit(w http.ResponseWriter, r *http.Request) error {
	file, err := bulkUpload(r, awardsFileField)
	if err != nil {
		if errors.Is(err, errNoUploadedFile) {
			return &webapp.Rejected{
				Template: awardBulkAddTemplate,
				Reasons:  []string{bulkFileReason},
			}
		}
		return err
	}
	defer file.Close()

	rows, err := csvRows(file)
	if err != nil {
		return &webapp.Rejected{
			Template: awardBulkAddTemplate,
			Reasons:  []string{fmt.Sprintf("The file could not be read: %s.", err)},
		}
	}
	if len(rows) == 0 {
		return &webapp.Rejected{
			Template: awardBulkAddTemplate,
			Reasons:  []string{"The file held a header row and nothing else."},
		}
	}

	var reasons []string
	for i, row := range rows {
		award := types.Award{
			Slug:      row["Slug"],
			DispTitle: row["Title"],
			DispSub:   row["Subtitle"],
		}
		if err := db.G[types.Award](u.DB).Create(r.Context(), &award); err != nil {
			slog.Warn("Could not create award", "slug", award.Slug, "error", err)
			reasons = append(reasons, fmt.Sprintf("Row %d (%s): %s", i+2, award.Slug, err))
		}
	}
	if len(reasons) > 0 {
		return &webapp.Rejected{
			Template: awardBulkAddTemplate,
			Reasons:  reportBulkFailures(reasons),
		}
	}

	u.SeeOther(w, r, "/award/")
	return nil
}

// oneAward loads an award by id, turning a missing row into the framework's
// not-found so a stale bookmark renders the 404 page rather than a 500.
func oneAward(ctx context.Context, d *db.DB, id uint) (types.Award, error) {
	award, err := db.G[types.Award](d).Where("id = ?", id).First(ctx)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return types.Award{}, fmt.Errorf("award %d: %w", id, webapp.ErrNotFound)
	case err != nil:
		return types.Award{}, fmt.Errorf("loading award %d: %w", id, err)
	}
	return award, nil
}

// reportBulkFailures keeps a rejection readable when a large file fails almost
// entirely: the first few reasons, then how many more there were.
func reportBulkFailures(reasons []string) []string {
	if len(reasons) <= maxReportedBulkFailures {
		return reasons
	}

	kept := reasons[:maxReportedBulkFailures]
	return append(kept, fmt.Sprintf("…and %d more rows like these.", len(reasons)-maxReportedBulkFailures))
}
