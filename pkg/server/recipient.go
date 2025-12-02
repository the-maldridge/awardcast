package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/flosch/pongo2/v6"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/the-maldridge/awardcast/pkg/types"
)

func (s *Server) uiViewRecipientList(w http.ResponseWriter, r *http.Request) {
	recipients := []types.Recipient{}
	res := s.d.Find(&recipients)
	if res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}

	s.doTemplate(w, r, "views/recipient/list.p2", pongo2.Context{"recipients": recipients})
}

func (s *Server) uiViewRecipientDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	id := s.strToUint(chi.URLParam(r, "id"))
	recipient := types.Recipient{}
	if res := s.d.Where("id = ?", id).First(&recipient); res.Error != nil && errors.Is(res.Error, gorm.ErrRecordNotFound) {
		slog.Warn("Error processing recipient", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	s.doTemplate(w, r, "views/recipient/delete.p2", pongo2.Context{"recipient": recipient})
}

func (s *Server) uiViewRecipientDelete(w http.ResponseWriter, r *http.Request) {
	id := s.strToUint(chi.URLParam(r, "id"))
	if res := s.d.Where(types.Winning{RecipientID: id}).Delete(&types.Winning{}); res.Error != nil {
		slog.Warn("Error processing recipient", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	if res := s.d.Where(types.Recipient{ID: id}).Delete(&types.Recipient{}); res.Error != nil {
		slog.Warn("Error processing recipient", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	http.Redirect(w, r, "/admin/recipient/", http.StatusSeeOther)
}

func (s *Server) uiViewRecipientBulkForm(w http.ResponseWriter, r *http.Request) {
	s.doTemplate(w, r, "views/recipient/bulk-add.p2", nil)
}

func (s *Server) uiViewRecipientBulkSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f, _, err := r.FormFile("recipients_file")
	if err != nil {
		slog.Warn("Error bulk adding recipients", "error", err)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": err.Error()})
		return
	}
	defer f.Close()
	recipients := s.csvToMap(f)

	for _, recipient := range recipients {
		res := s.d.Save(&types.Recipient{
			Name: recipient["Name"],
		})
		if res.Error != nil {
			slog.Warn("Could not create recipient", "error", res.Error)
		}
	}
	http.Redirect(w, r, "/admin/recipient/", http.StatusSeeOther)
}

func (s *Server) uiViewRecipientFormSingle(w http.ResponseWriter, r *http.Request) {
	id := s.strToUint(chi.URLParam(r, "id"))
	recipient := types.Recipient{}
	slog.Debug("recipient id", "id", id)
	if res := s.d.Where(&types.Recipient{ID: id}).Find(&recipient); res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		slog.Warn("Error processing recipient", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	s.doTemplate(w, r, "views/recipient/single.p2", pongo2.Context{"recipient": recipient})
}

func (s *Server) uiViewRecipientUpsert(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id := s.strToUint(chi.URLParam(r, "id"))
	recipient := types.Recipient{
		ID:   id,
		Name: r.FormValue("recipient_name"),
	}

	if res := s.d.Save(&recipient); res.Error != nil {
		slog.Warn("Error processing recipient", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	http.Redirect(w, r, "/admin/recipient/", http.StatusSeeOther)
}
