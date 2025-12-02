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

func (s *Server) uiViewAwardList(w http.ResponseWriter, r *http.Request) {
	awards := []types.Award{}
	res := s.d.Find(&awards)
	if res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}

	s.doTemplate(w, r, "views/award/list.p2", pongo2.Context{"awards": awards})
}

func (s *Server) uiViewAwardDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	id := s.strToUint(chi.URLParam(r, "id"))
	award := types.Award{}
	if res := s.d.Where("id = ?", id).First(&award); res.Error != nil && errors.Is(res.Error, gorm.ErrRecordNotFound) {
		slog.Warn("Error processing award", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	s.doTemplate(w, r, "views/award/delete.p2", pongo2.Context{"award": award})
}

func (s *Server) uiViewAwardDelete(w http.ResponseWriter, r *http.Request) {
	id := s.strToUint(chi.URLParam(r, "id"))
	if res := s.d.Where(types.Winning{AwardID: id}).Delete(&types.Winning{}); res.Error != nil {
		slog.Warn("Error processing award", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	if res := s.d.Where(types.Award{ID: id}).Delete(&types.Award{}); res.Error != nil {
		slog.Warn("Error processing award", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	http.Redirect(w, r, "/admin/award/", http.StatusSeeOther)
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

func (s *Server) uiViewAwardFormSingle(w http.ResponseWriter, r *http.Request) {
	id := s.strToUint(chi.URLParam(r, "id"))
	award := types.Award{}
	slog.Debug("award id", "id", id)
	if res := s.d.Where(&types.Award{ID: id}).Find(&award); res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		slog.Warn("Error processing award", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	s.doTemplate(w, r, "views/award/single.p2", pongo2.Context{"award": award})
}

func (s *Server) uiViewAwardUpsert(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id := s.strToUint(chi.URLParam(r, "id"))
	award := types.Award{
		ID:        id,
		Slug:      r.FormValue("award_slug"),
		DispTitle: r.FormValue("award_title"),
		DispSub:   r.FormValue("award_sub"),
	}

	if res := s.d.Save(&award); res.Error != nil {
		slog.Warn("Error processing award", "error", res.Error)
		s.doTemplate(w, r, "errors/request.p2", pongo2.Context{"error": res.Error})
		return
	}
	http.Redirect(w, r, "/admin/award/", http.StatusSeeOther)
}
