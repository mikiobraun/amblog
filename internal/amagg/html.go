package amagg

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"regexp"

	"atomicmicro.blog/internal/atom"
	"atomicmicro.blog/internal/web"
)

// Entries come from arbitrary feeds, so their HTML is untrusted. Pages are
// served with a CSP that forbids scripts, forms and plugins (sandbox), which
// makes rendering that HTML as is safe enough for a plain page.
const pageCSP = "default-src 'none'; img-src * data:; style-src 'unsafe-inline'; sandbox"

type threadPage struct {
	Aggregator string
	Title      string
	ThreadURL  string
	WebMention string // endpoint, advertised in the page head
	Posts      []postView
}

type postView struct {
	URL        string // this aggregator's page for the post
	AuthorName string
	AuthorURI  string
	Verified   bool
	From       string // host the entry was fetched from, shown for unverified entries
	Title      string
	IsHTML     bool
	HTML       template.HTML // untrusted, see pageCSP
	Text       string
	Published  string
	Via        string
	Depth      int  // reply depth in the thread, for indentation
	Current    bool // the post the page is about
}

func (s *Server) postView(e *Entry, ae *atom.Entry) postView {
	p := postView{
		URL:       s.postURL(e.N),
		Verified:  e.Verified,
		Title:     ae.Title,
		Published: ae.Published,
		Via:       linkHref(ae.Links, "via"),
	}
	if ae.Author != nil {
		p.AuthorName, p.AuthorURI = ae.Author.Name, ae.Author.URI
	}
	if u, err := url.Parse(e.FetchedFrom); err == nil {
		p.From = u.Host
	}
	if ae.Content != nil {
		if ae.Content.Type == "html" {
			body := ae.Content.Body
			if ae.InReplyTo != nil {
				body = nofollow(body)
			}
			p.IsHTML, p.HTML = true, template.HTML(body)
		} else {
			p.Text = ae.Content.Body
		}
	}
	return p
}

var anchorRe = regexp.MustCompile(`(?i)<a(\s|>)`)

// nofollow marks every link in a reply as user content, so links in replies
// carry no search ranking (see the spec, "Spam Control"). Our rel comes first,
// and browsers use the first of duplicate attributes.
func nofollow(html string) string {
	return anchorRe.ReplaceAllString(html, `<a rel="nofollow ugc"$1`)
}

func (s *Server) renderHTML(w http.ResponseWriter, t *template.Template, data any) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		web.ServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", pageCSP)
	w.Write(buf.Bytes())
}

const baseHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{block "title" .}}{{end}}</title>
{{block "head" .}}{{end}}
<style>
body { font: 16px/1.5 system-ui, sans-serif; max-width: 40rem; margin: 2rem auto; padding: 0 1rem; color: #222; background: #fff; }
@media (prefers-color-scheme: dark) { body { color: #ddd; background: #161616; } a { color: #8ab4f8; } }
.meta { color: #888; font-size: 0.9em; }
.text { white-space: pre-wrap; }
article { border-left: 2px solid #8884; padding-left: 0.75rem; margin: 1rem 0; }
article.current { border-left-color: #8ab4f8; }
</style>
</head>
<body>
{{block "body" .}}{{end}}
</body>
</html>`

var homeTemplate = template.Must(template.Must(template.New("base").Parse(baseHTML)).Parse(`
{{define "title"}}{{.Name}}{{end}}
{{define "body"}}
<h1>{{.Name}}</h1>
<p>An <a href="https://atomicmicro.blog/">Atomic Micro Blogging</a> aggregator.</p>
{{if .Public}}<p><a href="{{.Timeline}}">Island timeline</a> (Atom)</p>{{end}}
{{end}}`))

var postTemplate = template.Must(template.Must(template.New("base").Parse(baseHTML)).Parse(`
{{define "title"}}{{.Title}} · {{.Aggregator}}{{end}}
{{define "head"}}<link rel="webmention" href="{{.WebMention}}">{{end}}
{{define "body"}}
<p class="meta">{{.Aggregator}} · <a href="{{.ThreadURL}}">thread (Atom)</a></p>
{{range .Posts}}
<article{{if .Current}} class="current"{{end}} style="margin-left: {{.Depth}}rem">
  <p class="meta">
    {{if .Verified}}<a href="{{.AuthorURI}}">{{.AuthorName}}</a> ✓
    {{else}}{{.AuthorName}} · unverified · from {{.From}}{{end}}
  </p>
  {{if .Title}}<h2>{{.Title}}</h2>{{end}}
  {{if .IsHTML}}<div>{{.HTML}}</div>{{else}}<p class="text">{{.Text}}</p>{{end}}
  <p class="meta"><a href="{{.URL}}">{{.Published}}</a>{{if .Via}} · <a href="{{.Via}}">original</a>{{end}}</p>
</article>
{{end}}
{{end}}`))
