package server

import (
	"context"
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/flosch/pongo2/v6"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/the-maldridge/awardcast/pkg/types"
)

// Server contains the awards server and its associated machinery.
type Server struct {
	r chi.Router
	n *http.Server
	d *gorm.DB
	e *EventStream

	tmpls *pongo2.TemplateSet
}

//go:embed theme
var efs embed.FS

func New() (*Server, error) {
	var tfs fs.FS
	tfs, _ = fs.Sub(efs, "theme")
	if path, ok := os.LookupEnv("AWARDCAST_DEBUG"); ok {
		slog.Info("Debug mode enabled")
		tfs = os.DirFS(path)
	}
	tsfs, _ := fs.Sub(tfs, "p2")

	dbPath := os.Getenv("AWARDCAST_DB")
	if dbPath == "" {
		dbPath = "award.db"
	}
	d, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	err = d.AutoMigrate(
		&types.Award{},
		&types.Image{},
		&types.Recipient{},
		&types.Winning{},
	)
	if err != nil {
		return nil, err
	}

	s := &Server{
		r:     chi.NewRouter(),
		n:     &http.Server{},
		d:     d,
		e:     newES(),
		tmpls: pongo2.NewSet("html", pongo2.NewFSLoader(tsfs)),
	}

	s.r.Use(middleware.Heartbeat("/-/alive"))
	sfs, _ := fs.Sub(tfs, "static")
	s.r.Handle("/static/*", http.StripPrefix("/static", http.FileServerFS(sfs)))
	s.r.Route("/public", func(r chi.Router) {
		r.Get("/e", s.e.Handler)
		r.Get("/present", s.uiViewPresent)
		r.Route("/winnings", func(r chi.Router) {
			r.Get("/{id}/data", s.uiViewWinningData)
		})
	})
	s.r.Route("/admin", func(r chi.Router) {
		r.Get("/", s.uiViewAdminLanding)
		r.Route("/award", func(r chi.Router) {
			r.Get("/", s.uiViewAwardList)
			r.Get("/bulk-add", s.uiViewAwardBulkForm)
			r.Post("/bulk-add", s.uiViewAwardBulkSubmit)
		})
		r.Route("/recipient", func(r chi.Router) {
			r.Get("/", s.uiViewRecipientList)
			r.Get("/bulk-add", s.uiViewRecipientBulkForm)
			r.Post("/bulk-add", s.uiViewRecipientBulkSubmit)
		})
		r.Route("/winnings", func(r chi.Router) {
			r.Get("/", s.uiViewWinningList)
			r.Get("/assign", s.uiViewWinningAssignForm)
			r.Post("/assign", s.uiViewWinningAssignSubmit)
			r.Get("/{id}/present", s.uiViewWinningPresent)
			r.Get("/{id}/reveal", s.uiViewWinningReveal)
			r.Get("/clear-board", s.uiViewWinningClearBoard)
		})
	})

	s.r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/public/present", http.StatusSeeOther)
	})
	return s, nil
}

// Serve binds, initializes the mux, and serves forever.
func (s *Server) Serve(bind string) error {
	slog.Info("HTTP is starting")
	s.n.Addr = bind
	s.n.Handler = s.r
	return s.n.ListenAndServe()
}

// Shutdown requests the underlying server gracefully cease operation.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.n.Shutdown(ctx)
}

func (s *Server) templateErrorHandler(w http.ResponseWriter, err error) {
	fmt.Fprintf(w, "Error while rendering template: %s\n", err)
}

func (s *Server) doTemplate(w http.ResponseWriter, r *http.Request, tmpl string, ctx pongo2.Context) {
	if ctx == nil {
		ctx = pongo2.Context{}
	}
	t, err := s.tmpls.FromCache(tmpl)
	if err != nil {
		s.templateErrorHandler(w, err)
		return
	}
	if err := t.ExecuteWriter(ctx, w); err != nil {
		s.templateErrorHandler(w, err)
	}
}

func (s *Server) uiViewPresent(w http.ResponseWriter, r *http.Request) {
	s.doTemplate(w, r, "present.p2", nil)
}

func (s *Server) uiViewAdminLanding(w http.ResponseWriter, r *http.Request) {
	s.doTemplate(w, r, "views/admin/landing.p2", nil)
}

func (s *Server) csvToMap(reader io.Reader) []map[string]string {
	r := csv.NewReader(reader)
	rows := []map[string]string{}
	var header []string
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Error("Error decoding CSV", "error", err)
			return nil
		}
		if header == nil {
			header = record
		} else {
			dict := map[string]string{}
			for i := range header {
				dict[header[i]] = record[i]
			}
			rows = append(rows, dict)
		}
	}
	return rows
}

func (s *Server) strToUint(st string) uint {
	int, err := strconv.Atoi(st)
	if err != nil {
		return 0
	}
	return uint(int)
}
