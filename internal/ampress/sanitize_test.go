package ampress

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"golang.org/x/net/html"
)

const markdownSample = `# Heading

A paragraph with *em*, **strong**, ~~gone~~, ` + "`code`" + `, a [link](https://example.com/a?b=1&c=2 "Title"),
a footnote[^1] and an autolink https://example.com/auto.

![An image](https://example.com/img.png "Image title")

> A quote
> over two lines

1. First
2. Second
   - nested
   - list

3. Loose

- [x] done
- [ ] todo

| Left | Center | Right |
|:-----|:------:|------:|
| a    | b      | c     |

` + "```go\nfunc main() {\n\tfmt.Println(\"<hi>\")\n}\n```" + `

---

Line one
line two

[^1]: The note.
`

// HTML that comes from Markdown stays intact: both goldmark's (as amfeed's
// content folder renders it) and the footnote extension's.
func TestSanitizeKeepsMarkdown(t *testing.T) {
	var buf bytes.Buffer
	md := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote))
	if err := md.Convert([]byte(markdownSample), &buf); err != nil {
		t.Fatal(err)
	}
	in := buf.String()
	inLines, outLines := strings.Split(normalize(in), "\n"), strings.Split(normalize(sanitize(in)), "\n")
	for i := range max(len(inLines), len(outLines)) {
		var a, b string
		if i < len(inLines) {
			a = inLines[i]
		}
		if i < len(outLines) {
			b = outLines[i]
		}
		if a != b {
			t.Errorf("line %d changed:\n  in:  %s\n  out: %s", i+1, a, b)
		}
	}
}

// normalize writes HTML in one notation, so equal HTML compares equal: tokens
// re-escaped the same way, and spaces in style values removed.
func normalize(s string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	for z.Next() != html.ErrorToken {
		tok := z.Token()
		for i, a := range tok.Attr {
			if a.Key == "style" {
				tok.Attr[i].Val = strings.ReplaceAll(a.Val, " ", "")
			}
		}
		b.WriteString(tok.String())
	}
	return b.String()
}

func TestSanitizeDrops(t *testing.T) {
	for in, want := range map[string]string{
		`<p>Hi<script>alert(1)</script></p>`:                                         `<p>Hi</p>`,
		`<p onclick="alert(1)" style="color:red">Hi</p>`:                             `<p>Hi</p>`,
		`<style>body{display:none}</style><p>Hi</p>`:                                 `<p>Hi</p>`,
		`<a href="javascript:alert(1)">x</a>`:                                        `x`,
		`<img src="data:image/png;base64,AAAA" alt="x">`:                             `<img alt="x">`,
		`<iframe src="https://evil.example/"></iframe><p>Hi</p>`:                     `<p>Hi</p>`,
		`<form action="/"><input type="text" name="q"></form>`:                       ``,
		`<base href="https://evil.example/"><meta http-equiv="refresh" content="0">`: ``,
		`<p class="hint reply">Hi</p>`:                                               `<p>Hi</p>`,
		`<div class="h-entry"><p class="e-content">Hi</p></div>`:                     `<div class="h-entry"><p class="e-content">Hi</p></div>`,
		`<td style="text-align:center;position:fixed">x</td>`:                        `<td style="text-align: center">x</td>`,
		`<a href="https://example.com/">x</a>`:                                       `<a href="https://example.com/">x</a>`,
	} {
		if got := strings.TrimSpace(sanitize(in)); got != want {
			t.Errorf("sanitize(%s) = %s, want %s", in, got, want)
		}
	}
}
