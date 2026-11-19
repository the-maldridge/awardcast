package server

import (
	"io/fs"
	"path"
	"strings"
	"testing"

	"github.com/the-maldridge/webapp-bulma"
	"github.com/the-maldridge/webapp/tmpl"
)

// TestEveryTemplateCompiles parses every page in the embedded theme the way
// the first render would.
//
// Templates are compiled on first use and cached, so a syntax error is not a
// startup failure: it is a 500 on that one page, which is how a template nobody
// clicked during review arrives at the ceremony it was edited for. Mounting the
// same two roots the application mounts — these pages under their own namespace
// and the theme's under the framework's — and asking the renderer to verify them
// moves that failure into the suite, where pongo2 names the file, the line, and
// what it choked on. Compiling also resolves every extends, so a page reaching
// for a layout or a fragment that is not there fails here too.
func TestEveryTemplateCompiles(t *testing.T) {
	pages, err := fs.Sub(assets, "theme/p2")
	if err != nil {
		t.Fatalf("embedded pages: %v", err)
	}

	// The same registry shape installTemplates builds: application pages
	// first, the selected theme as core. Nothing under AWARDCAST_THEME_DIR
	// is consulted — the copy that ships in the binary is the copy checked.
	reg := tmpl.New()
	if err := reg.Mount(templatesNS, pages); err != nil {
		t.Fatalf("mounting %q: %v", templatesNS, err)
	}
	if err := reg.Mount("core", bulma.FS()); err != nil {
		t.Fatalf("mounting the bulma theme: %v", err)
	}

	render, err := tmpl.NewRenderer(reg)
	if err != nil {
		t.Fatalf("building the renderer: %v", err)
	}

	var names []string
	err = fs.WalkDir(pages, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".p2") {
			names = append(names, "@"+templatesNS+"/"+path.Clean(p))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded pages: %v", err)
	}
	if len(names) < 10 {
		t.Fatalf("found only %d templates under theme/p2; the walk is broken, not the theme", len(names))
	}

	if err := render.Verify(names...); err != nil {
		t.Errorf("an embedded template does not compile, which on first render is a 500:\n%v", err)
	}
}
