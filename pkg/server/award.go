package server

import (
	"log/slog"
	"net/http"

	"github.com/flosch/pongo2/v6"

	"github.com/the-maldridge/awardcast/pkg/types"
)

func (s *Server) uiViewAwardList(w http.ResponseWriter, r *http.Request) {
	awards := []types.Award{}
	res := s.d.Find(&awards)
	if res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}

	s.doTemplate(w, r, "views/award/list.p2", pongo2.Context{"awards": awards})
}

func (s *Server) uiViewAwardBulkForm(w http.ResponseWriter, r *http.Request) {
	s.doTemplate(w, r, "views/award/bulk-add.p2", nil)
}

func (s *Server) uiViewAwardBulkSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f, _, err := r.FormFile("awards_file")
	if err != nil {
		slog.Warn("Error bulk adding awards", "error", err)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": err.Error()})
		return
	}
	defer f.Close()
	awards := s.csvToMap(f)

	for _, award := range awards {
		res := s.d.Save(&types.Award{
			Slug:      award["Slug"],
			DispTitle: award["Title"],
			DispSub:   award["Subtitle"],
		})
		if res.Error != nil {
			slog.Warn("Could not create award", "error", res.Error)
		}
	}
	http.Redirect(w, r, "/admin/award/", http.StatusSeeOther)
}
