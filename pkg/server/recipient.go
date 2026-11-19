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
	recipientListTemplate    = "@awardcast/views/recipient/list.p2"
	recipientFormTemplate    = "@awardcast/views/recipient/single.p2"
	recipientDeleteTemplate  = "@awardcast/views/recipient/delete.p2"
	recipientBulkAddTemplate = "@awardcast/views/recipient/bulk-add.p2"

	recipientsFileField = "recipients_file"
)

func (u *ui) uiViewRecipientList(w http.ResponseWriter, r *http.Request) error {
	recipients, err := db.G[types.Recipient](u.DB).Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing recipients: %w", err)
	}

	u.View(w, r, recipientListTemplate, webapp.Context{"recipients": recipients})
	return nil
}

// uiViewRecipientFormSingle serves the add form and the edit form, with the
// same reason as the award form that a named-but-missing recipient is a 404.
func (u *ui) uiViewRecipientFormSingle(w http.ResponseWriter, r *http.Request) error {
	id, err := formID(r)
	if err != nil {
		return err
	}

	recipient := types.Recipient{}
	if id != 0 {
		if recipient, err = oneRecipient(r.Context(), u.DB, id); err != nil {
			return err
		}
	}

	u.View(w, r, recipientFormTemplate, webapp.Context{"recipient": recipient})
	return nil
}

func (u *ui) uiViewRecipientUpsert(w http.ResponseWriter, r *http.Request) error {
	id, err := formID(r)
	if err != nil {
		return err
	}
	name, err := webapp.FormString(r, "recipient_name")
	if err != nil {
		return err
	}

	recipient := types.Recipient{ID: id, Name: name}
	if err := db.InsertOrUpdate(r.Context(), u.DB, &recipient, db.OnConflictColumns("id")); err != nil {
		return fmt.Errorf("saving recipient %q: %w", name, err)
	}

	u.SeeOther(w, r, "/recipient/")
	return nil
}

func (u *ui) uiViewRecipientDeleteConfirm(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	recipient, err := oneRecipient(r.Context(), u.DB, id)
	if err != nil {
		return err
	}

	u.View(w, r, recipientDeleteTemplate, webapp.Context{"recipient": recipient})
	return nil
}

func (u *ui) uiViewRecipientDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}

	// Deleting a recipient who has won something takes the winnings with them,
	// in one transaction: the alternative is a winning whose winner is a name
	// the presenter would show as blank.
	err = db.Transaction(r.Context(), u.DB, func(tx *gorm.DB) error {
		if _, err := gorm.G[types.Winning](tx).Where("recipient_id = ?", id).Delete(r.Context()); err != nil {
			return fmt.Errorf("deleting the winnings for recipient %d: %w", id, err)
		}
		if _, err := gorm.G[types.Recipient](tx).Where("id = ?", id).Delete(r.Context()); err != nil {
			return fmt.Errorf("deleting recipient %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	u.SeeOther(w, r, "/recipient/")
	return nil
}

func (u *ui) uiViewRecipientBulkForm(w http.ResponseWriter, r *http.Request) error {
	u.View(w, r, recipientBulkAddTemplate, nil)
	return nil
}

func (u *ui) uiViewRecipientBulkSubmit(w http.ResponseWriter, r *http.Request) error {
	file, err := bulkUpload(r, recipientsFileField)
	if err != nil {
		if errors.Is(err, errNoUploadedFile) {
			return &webapp.Rejected{
				Template: recipientBulkAddTemplate,
				Reasons:  []string{bulkFileReason},
			}
		}
		return err
	}
	defer file.Close()

	rows, err := csvRows(file)
	if err != nil {
		return &webapp.Rejected{
			Template: recipientBulkAddTemplate,
			Reasons:  []string{fmt.Sprintf("The file could not be read: %s.", err)},
		}
	}
	if len(rows) == 0 {
		return &webapp.Rejected{
			Template: recipientBulkAddTemplate,
			Reasons:  []string{"The file held a header row and nothing else."},
		}
	}

	var reasons []string
	for i, row := range rows {
		recipient := types.Recipient{Name: row["Name"]}
		if err := db.G[types.Recipient](u.DB).Create(r.Context(), &recipient); err != nil {
			slog.Warn("Could not create recipient", "name", recipient.Name, "error", err)
			reasons = append(reasons, fmt.Sprintf("Row %d (%s): %s", i+2, recipient.Name, err))
		}
	}
	if len(reasons) > 0 {
		return &webapp.Rejected{
			Template: recipientBulkAddTemplate,
			Reasons:  reportBulkFailures(reasons),
		}
	}

	u.SeeOther(w, r, "/recipient/")
	return nil
}

// oneRecipient loads a recipient by id, turning a missing row into the
// framework's not-found.
func oneRecipient(ctx context.Context, d *db.DB, id uint) (types.Recipient, error) {
	recipient, err := db.G[types.Recipient](d).Where("id = ?", id).First(ctx)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return types.Recipient{}, fmt.Errorf("recipient %d: %w", id, webapp.ErrNotFound)
	case err != nil:
		return types.Recipient{}, fmt.Errorf("loading recipient %d: %w", id, err)
	}
	return recipient, nil
}
