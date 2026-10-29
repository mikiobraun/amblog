package ampress

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// HTML from feeds is data, not a page: it is reduced to an allowlist of
// structure before display (see the spec, "ampress — Blog"). Scripts, styles,
// embeds and forms go; what Markdown produces stays intact. The stored entry
// keeps its HTML as signed; only the display is cleaned.
var sanitizer = newSanitizer()

var (
	// Classes: microformats2 (h-entry, p-name, u-url, dt-published,
	// e-content, ...), code languages (language-go) and footnotes.
	classRe = regexp.MustCompile(`^\s*(?:(?:(?:h|p|u|dt|e)-[a-z0-9]+(?:-[a-z0-9]+)*|language-[\w+#-]+|footnotes?(?:-[a-z]+)*|reversefootnote)\s*)+$`)
	// Anchors for footnotes and headings.
	idRe   = regexp.MustCompile(`^[\w:.-]+$`)
	roleRe = regexp.MustCompile(`^doc-[a-z]+$`)
)

func newSanitizer() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowStandardURLs()           // http, https, mailto and relative (e.g. #fn:1); no javascript: or data:
	p.RequireNoFollowOnLinks(false) // replies get rel="nofollow ugc" separately; posts keep their links as is
	p.AllowElements(
		// text
		"p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre", "code",
		"em", "strong", "b", "i", "u", "s", "del", "ins", "mark", "small", "sub", "sup",
		"kbd", "samp", "var", "abbr", "cite", "q", "dfn", "time", "span", "div", "wbr",
		// lists
		"ul", "ol", "li", "dl", "dt", "dd",
		// structure
		"section", "article", "aside", "header", "footer", "nav", "main",
		"figure", "figcaption", "details", "summary",
		// tables
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "colgroup", "col",
	)
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("src", "alt", "width", "height").OnElements("img")
	p.AllowAttrs("title").Globally()
	p.AllowAttrs("lang", "dir").Globally()
	p.AllowAttrs("class").Matching(classRe).Globally()
	p.AllowAttrs("id").Matching(idRe).Globally()
	p.AllowAttrs("role").Matching(roleRe).Globally()
	p.AllowAttrs("datetime").OnElements("time", "del", "ins")
	p.AllowAttrs("cite").OnElements("blockquote", "q", "del", "ins")
	p.AllowAttrs("start", "reversed", "type").OnElements("ol")
	p.AllowAttrs("open").OnElements("details")
	p.AllowAttrs("colspan", "rowspan", "scope", "headers", "align").OnElements("th", "td")
	p.AllowAttrs("span").OnElements("col", "colgroup")
	// Table column alignment, as goldmark writes it.
	p.AllowStyles("text-align").MatchingEnum("left", "right", "center").OnElements("th", "td")
	// Task lists: disabled checkboxes, outside any form.
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	return p
}

// sanitize reduces feed HTML to the allowlist.
func sanitize(html string) string { return sanitizer.Sanitize(html) }
