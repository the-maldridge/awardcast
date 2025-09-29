package server

import (
	"encoding/json"
	"net/http"

	"github.com/flosch/pongo2/v6"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm/clause"

	"github.com/the-maldridge/awardcast/pkg/types"
)

// winningMessage is used to pass the actions to the frontend.
type winningMessage struct {
	Type uint
	WinID uint
}

func (s *Server) uiViewWinningList(w http.ResponseWriter, r *http.Request) {
	winnings := []types.Winning{}
	res := s.d.Preload(clause.Associations).Find(&winnings)
	if res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}

	s.doTemplate(w, r, "views/winning/list.p2", pongo2.Context{"winnings": winnings})
}

func (s *Server) uiViewWinningAssignForm(w http.ResponseWriter, r *http.Request) {
	awards := []types.Award{}
	res := s.d.Find(&awards)
	if res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}

	recipients := []types.Recipient{}
	res = s.d.Find(&recipients)
	if res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}

	ctx := pongo2.Context{
		"awards":     awards,
		"recipients": recipients,
	}
	s.doTemplate(w, r, "views/winning/form.p2", ctx)
}

func (s *Server) uiViewWinningAssignSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	winning := types.Winning{
		AwardID:     s.strToUint(r.FormValue("award")),
		RecipientID: s.strToUint(r.FormValue("recipient")),
	}

	if res := s.d.Save(&winning); res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}

	http.Redirect(w, r, "/admin/winnings/", http.StatusSeeOther)
}

func (s *Server) uiViewWinningData(w http.ResponseWriter, r *http.Request) {
	win := types.Winning{}
	res := s.d.Preload(clause.Associations).Where(&types.Winning{ID: s.strToUint(chi.URLParam(r, "id"))}).First(&win)
	if res.Error != nil {
		s.doTemplate(w, r, "views/errors/internal.p2", pongo2.Context{"error": res.Error})
		return
	}
	if err := json.NewEncoder(w).Encode(win); err != nil {
		w.Write([]byte(err.Error()))
		return
	}
}

func (s *Server) uiViewWinningPresent(w http.ResponseWriter, r *http.Request) {
	bytes, err := json.Marshal(&winningMessage{Type: 1, WinID: s.strToUint(chi.URLParam(r, "id"))})
	if err != nil {
		return
	}
	s.e.publish(bytes)
}

func (s *Server) uiViewWinningReveal(w http.ResponseWriter, r *http.Request) {
	bytes, err := json.Marshal(&winningMessage{Type: 2})
	if err != nil {
		return
	}
	s.e.publish(bytes)
}
