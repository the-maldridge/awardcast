package server

import (
	"log/slog"
	"net/http"

	"github.com/flosch/pongo2/v6"

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
