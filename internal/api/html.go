package api

import (
	"html/template"
	"net/http"
)

// HTML views are server-rendered with html/template and no client-side
// dependencies. They're designed to iframe-embed cleanly inside review
// apps (e.g. radical-roc) — no nav chrome, no fonts, no JS. The shared
// base layout below keeps the visual treatment consistent across the
// /m, /t, /p, /l, /attachment views without imposing any framework.

const htmlBaseStyles = `
:root { color-scheme: light dark; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  margin: 0;
  padding: 1rem 1.25rem;
  line-height: 1.45;
}
header.mv {
  border-bottom: 1px solid color-mix(in srgb, currentColor 15%, transparent);
  margin-bottom: 1rem;
  padding-bottom: 0.5rem;
}
header.mv h1 { margin: 0 0 0.25rem; font-size: 1.1rem; font-weight: 600; }
header.mv .mv-meta { font-size: 0.8rem; opacity: 0.7; }
.mv-message {
  border: 1px solid color-mix(in srgb, currentColor 12%, transparent);
  border-radius: 6px;
  padding: 0.75rem 1rem;
  margin-bottom: 0.75rem;
}
.mv-message-head { display: flex; justify-content: space-between; gap: 1rem; font-size: 0.85rem; margin-bottom: 0.5rem; }
.mv-from { font-weight: 500; }
.mv-snippet { font-size: 0.9rem; opacity: 0.85; }
.mv-empty { font-style: italic; opacity: 0.6; }
.mv-not-found {
  border: 1px dashed color-mix(in srgb, currentColor 25%, transparent);
  padding: 1rem 1.25rem;
  border-radius: 6px;
}
.mv-error {
  border: 1px solid #c44;
  padding: 1rem 1.25rem;
  border-radius: 6px;
  color: #c44;
}
.mv-id { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.75rem; opacity: 0.6; }
.mv-participants { font-size: 0.85rem; margin-top: 0.25rem; opacity: 0.85; }
`

// htmlBaseLayout is wrapped around every concrete view. The {{template
// "body" .}} clause is filled in by each view's inner template via the
// ParseFS-style multi-template construction below.
const htmlBaseLayout = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{block "title" .}}msgvault{{end}}</title>
<style>` + htmlBaseStyles + `</style>
</head>
<body>
{{block "body" .}}{{end}}
</body>
</html>`

// threadViewTemplate renders APIThread inside the shared layout.
var threadViewTemplate = template.Must(template.New("thread").Parse(htmlBaseLayout))

// messageViewTemplate renders messageViewData inside the shared layout.
var messageViewTemplate = template.Must(template.New("message").Parse(htmlBaseLayout))

// attachmentViewTemplate renders attachmentViewData inside the shared layout.
var attachmentViewTemplate = template.Must(template.New("attachment").Parse(htmlBaseLayout))

// attachmentViewData drives the /attachment/{id} preview page.
//
// PreviewMode controls the inline rendering strategy:
//
//	"pdf"   → <iframe src="/api/v1/attachments/{id}/content">
//	"image" → <img src="/api/v1/attachments/{id}/content">
//	"text"  → <iframe> the content (browser will render text/plain)
//	"none"  → download CTA only
type attachmentViewData struct {
	ID          int64
	MessageID   int64
	Filename    string
	MimeType    string
	Size        int64
	PreviewMode string
	ContentURL  string
}

// messageViewData carries the fields the /m/{id} template needs. Bodies
// are split: BodyHTML rendered as-is (already trusted msgvault-stored
// content), BodyText shown only when no HTML part exists.
type messageViewData struct {
	ID             int64
	ConversationID int64
	Subject        string
	From           string
	To             []string
	Cc             []string
	SentAt         string
	BodyHTML       template.HTML
	BodyText       string
	Attachments    []messageAttachmentView
}

type messageAttachmentView struct {
	Filename string
	MimeType string
	Size     int64
}

func init() {
	// Inner templates are defined separately so the shared base layout
	// keeps a single source of truth.
	template.Must(threadViewTemplate.New("title").Parse(
		`{{if .Subject}}{{.Subject}}{{else}}Thread {{.ID}}{{end}} — msgvault`,
	))
	template.Must(threadViewTemplate.New("body").Parse(`
<header class="mv">
  <h1>{{if .Subject}}{{.Subject}}{{else}}(no subject){{end}}</h1>
  <div class="mv-meta">
    <span class="mv-id">thread {{.ID}}</span>
    · {{.MessageCount}} message{{if ne .MessageCount 1}}s{{end}}
  </div>
  {{if .Participants}}
  <div class="mv-participants">
    {{range $i, $p := .Participants}}{{if $i}}, {{end}}{{if $p.Name}}{{$p.Name}} &lt;{{$p.Address}}&gt;{{else}}{{$p.Address}}{{end}}{{end}}
  </div>
  {{end}}
</header>
{{if .Messages}}
  {{range .Messages}}
  <div class="mv-message">
    <div class="mv-message-head">
      <span class="mv-from">{{if .FromName}}{{.FromName}} &lt;{{.From}}&gt;{{else}}{{.From}}{{end}}</span>
      <span class="mv-meta">{{.SentAt.Format "2006-01-02 15:04 MST"}} · <a href="/m/{{.ID}}">open</a></span>
    </div>
    {{if .Snippet}}<div class="mv-snippet">{{.Snippet}}</div>{{end}}
  </div>
  {{end}}
{{else}}
  <p class="mv-empty">No live messages in this thread.</p>
{{end}}
`))
}

func init() {
	template.Must(messageViewTemplate.New("title").Parse(
		`{{if .Subject}}{{.Subject}}{{else}}Message {{.ID}}{{end}} — msgvault`,
	))
	template.Must(messageViewTemplate.New("body").Parse(`
<header class="mv">
  <h1>{{if .Subject}}{{.Subject}}{{else}}(no subject){{end}}</h1>
  <div class="mv-meta">
    <span class="mv-id">message {{.ID}}</span>
    {{if .ConversationID}}· <a href="/t/{{.ConversationID}}">thread {{.ConversationID}}</a>{{end}}
    · {{.SentAt}}
  </div>
  <div class="mv-meta">
    <div><strong>From:</strong> {{.From}}</div>
    {{if .To}}<div><strong>To:</strong> {{range $i, $a := .To}}{{if $i}}, {{end}}{{$a}}{{end}}</div>{{end}}
    {{if .Cc}}<div><strong>Cc:</strong> {{range $i, $a := .Cc}}{{if $i}}, {{end}}{{$a}}{{end}}</div>{{end}}
  </div>
</header>
<section class="mv-body">
  {{if .BodyHTML}}
    {{.BodyHTML}}
  {{else if .BodyText}}
    <pre style="white-space: pre-wrap; font-family: inherit; margin: 0;">{{.BodyText}}</pre>
  {{else}}
    <p class="mv-empty">(no body)</p>
  {{end}}
</section>
{{if .Attachments}}
<aside class="mv-meta" style="margin-top: 1rem;">
  <strong>Attachments:</strong>
  <ul>
    {{range .Attachments}}
    <li>{{.Filename}} <span class="mv-id">({{.MimeType}}, {{.Size}} bytes)</span></li>
    {{end}}
  </ul>
</aside>
{{end}}
`))
}

func init() {
	template.Must(attachmentViewTemplate.New("title").Parse(
		`{{if .Filename}}{{.Filename}}{{else}}Attachment {{.ID}}{{end}} — msgvault`,
	))
	template.Must(attachmentViewTemplate.New("body").Parse(`
<header class="mv">
  <h1>{{if .Filename}}{{.Filename}}{{else}}Attachment {{.ID}}{{end}}</h1>
  <div class="mv-meta">
    <span class="mv-id">attachment {{.ID}}</span>
    {{if .MessageID}}· <a href="/m/{{.MessageID}}">message {{.MessageID}}</a>{{end}}
    · {{.MimeType}} · {{.Size}} bytes
  </div>
</header>
{{if eq .PreviewMode "pdf"}}
  <iframe src="{{.ContentURL}}" style="width: 100%; height: 80vh; border: 1px solid color-mix(in srgb, currentColor 15%, transparent); border-radius: 6px;"></iframe>
{{else if eq .PreviewMode "image"}}
  <img src="{{.ContentURL}}" alt="{{.Filename}}" style="max-width: 100%; height: auto; border-radius: 6px;">
{{else if eq .PreviewMode "text"}}
  <iframe src="{{.ContentURL}}" style="width: 100%; height: 60vh; border: 1px solid color-mix(in srgb, currentColor 15%, transparent); border-radius: 6px;"></iframe>
{{else}}
  <p>This attachment type isn't previewed inline.</p>
  <p><a href="{{.ContentURL}}">Download {{.Filename}}</a></p>
{{end}}
`))
}

// writeHTMLNotFound renders a 404 page using the shared layout. Used by HTML
// views (/m, /t, ...) when a path id doesn't resolve. The detail string is
// rendered as text so callers may pass a corpus-mismatch hint without
// risking injection.
func writeHTMLNotFound(w http.ResponseWriter, detail string) {
	writeHTMLStatus(w, http.StatusNotFound, "Not found", detail, "mv-not-found")
}

// writeHTMLError renders a generic error page with the given status.
func writeHTMLError(w http.ResponseWriter, status int, detail string) {
	writeHTMLStatus(w, status, "Error", detail, "mv-error")
}

func writeHTMLStatus(w http.ResponseWriter, status int, title, detail, class string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	tmpl := template.Must(template.New("status").Parse(htmlBaseLayout))
	template.Must(tmpl.New("title").Parse(`{{.Title}} — msgvault`))
	template.Must(tmpl.New("body").Parse(
		`<header class="mv"><h1>{{.Title}}</h1></header><div class="{{.Class}}">{{.Detail}}</div>`,
	))
	_ = tmpl.Execute(w, struct{ Title, Detail, Class string }{title, detail, class})
}
