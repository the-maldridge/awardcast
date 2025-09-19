package server

import (
	"net/http"
)

func (s *Server) uiViewAwardList(w http.ResponseWriter, r *http.Request) {
	s.doTemplate(w, r, "views/award/list.p2", nil)
}

func (s *Server) uiViewAwardBulkForm(w http.ResponseWriter, r *http.Request) {
	s.doTemplate(w, r, "views/award/bulk-add.p2", nil)
}

func (s *Server) uiViewAwardBulkSubmit(w http.ResponseWriter, r *http.Request) {

}
