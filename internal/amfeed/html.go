package amfeed

import (
	"bytes"
	"html/template"
	"net/http"
)

type pageData struct {
	Title      string
	AuthorName string
	AuthorURI  string
	FeedURL    string
	KeysURL    string
	WebMention string // endpoint, advertised on every page
	Entries    []pageEntry
}

type pageEntry struct {
	URL       string
	Published string
	Title     string
	IsHTML    bool
	Text      string
	HTML      template.HTML // owner-authored, trusted
	InReplyTo string
	Deleted   bool
}

func (s *Server) pageData(entries []*Entry) pageData {
	d := pageData{
		Title:      s.cfg.Feed.Title,
		AuthorName: s.cfg.Feed.AuthorName,
		AuthorURI:  s.cfg.Feed.AuthorURI,
		FeedURL:    s.feedURL(),
		KeysURL:    s.keysURL,
		WebMention: s.webmentionURL(),
	}
	for _, e := range entries {
		pe := pageEntry{
			URL:       s.postURL(e.Seq),
			Published: e.Published,
			Title:     e.Title,
			IsHTML:    e.ContentType == "html",
			Text:      e.Content,
			HTML:      template.HTML(e.Content),
			InReplyTo: e.InReplyToHref,
			Deleted:   e.Deleted,
		}
		if pe.InReplyTo == "" {
			pe.InReplyTo = e.InReplyToRef
		}
		d.Entries = append(d.Entries, pe)
	}
	return d
}

func (s *Server) renderHTML(w http.ResponseWriter, status int, t *template.Template, data pageData) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

const baseHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="alternate" type="application/atom+xml" href="{{.FeedURL}}">
<link rel="webmention" href="{{.WebMention}}">
<style>
body { font-family: system-ui, sans-serif; max-width: 40rem; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; }
article { border-top: 1px solid #ddd; padding: 1rem 0; }
.meta { color: #777; font-size: 0.85rem; }
.text { white-space: pre-wrap; }
</style>
</head>
<body>
{{block "body" .}}{{end}}
</body>
</html>`

const entriesHTML = `{{define "entries"}}{{range .Entries}}
<article>
  {{if .Deleted}}<p class="meta">This post was deleted.</p>{{else}}
  {{if .InReplyTo}}<p class="meta">↳ in reply to <a href="{{.InReplyTo}}">{{.InReplyTo}}</a></p>{{end}}
  {{if .Title}}<h2>{{.Title}}</h2>{{end}}
  {{if .IsHTML}}<div>{{.HTML}}</div>{{else}}<p class="text">{{.Text}}</p>{{end}}
  {{end}}
  <p class="meta"><a href="{{.URL}}">{{.Published}}</a></p>
</article>
{{end}}{{end}}`

var profileTemplate = template.Must(template.Must(template.New("base").Parse(baseHTML)).Parse(entriesHTML + `
{{define "body"}}
<h1>{{.AuthorName}}</h1>
<p class="meta"><a href="{{.FeedURL}}">Atom feed</a> · <a href="{{.KeysURL}}">signing keys</a></p>
{{template "entries" .}}
{{end}}`))

var postTemplate = template.Must(template.Must(template.New("base").Parse(baseHTML)).Parse(entriesHTML + `
{{define "body"}}
<p class="meta"><a href="{{.AuthorURI}}">{{.AuthorName}}</a></p>
{{template "entries" .}}
{{end}}`))
