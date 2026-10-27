package ampress

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
)

// The built-in theme. A theme folder overrides it file by file (see the
// spec, "Themes"): templates base.html, index.html, post.html, about.html,
// notfound.html, and static files under static/. Each page template is
// parsed together with base.html and executed as base.html.
//
//go:embed theme
var builtin embed.FS

var pageNames = []string{"index", "post", "about", "notfound"}

type theme struct {
	dir   string // empty: built-in only
	pages map[string]*template.Template
}

func loadTheme(dir string) (*theme, error) {
	th := &theme{dir: dir, pages: map[string]*template.Template{}}
	base, err := th.read("base.html")
	if err != nil {
		return nil, err
	}
	for _, name := range pageNames {
		src, err := th.read(name + ".html")
		if err != nil {
			return nil, err
		}
		t, err := template.New("base.html").Parse(base)
		if err != nil {
			return nil, fmt.Errorf("theme: base.html: %w", err)
		}
		if _, err := t.Parse(src); err != nil {
			return nil, fmt.Errorf("theme: %s.html: %w", name, err)
		}
		th.pages[name] = t
	}
	return th, nil
}

// read returns a template file from the theme folder, or else the built-in one.
func (th *theme) read(name string) (string, error) {
	if th.dir != "" {
		b, err := os.ReadFile(filepath.Join(th.dir, name))
		if err == nil {
			return string(b), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("theme: %w", err)
		}
	}
	b, err := builtin.ReadFile("theme/" + name)
	return string(b), err
}

// static returns the file system that serves the static file name: the theme
// folder's static/ if it has the file, or else the built-in one.
func (th *theme) static(name string) fs.FS {
	if th.dir != "" {
		own := os.DirFS(filepath.Join(th.dir, "static"))
		if _, err := fs.Stat(own, name); err == nil {
			return own
		}
	}
	sub, _ := fs.Sub(builtin, "theme/static")
	return sub
}
