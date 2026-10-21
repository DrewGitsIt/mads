package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// exportSchema versions the JSON wrapper GET /sessions/{id}/export?format=json
// emits. Bump it (v2) on any breaking change to the shape below.
const exportSchema = "mads.export/v1"

// exportDoc is the whole export: the session, its conversation, and every
// delegate's own conversation. The JSON format is this struct verbatim; the
// Markdown format is a rendering of it.
type exportDoc struct {
	Schema     string                        `json:"schema"`
	ExportedAt time.Time                     `json:"exported_at"`
	Session    exportSession                 `json:"session"`
	Messages   []contracts.TranscriptMessage `json:"messages"`
	Delegates  []exportDelegate              `json:"delegates"`
}

type exportSession struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// exportDelegate is one subagent: ID is its harness id (the id its own
// transcript is filed under), Name its description, falling back to its type.
type exportDelegate struct {
	ID       string                        `json:"id"`
	Name     string                        `json:"name"`
	Type     string                        `json:"type,omitempty"`
	Messages []contracts.TranscriptMessage `json:"messages"`
}

// handleExportSession serves the whole conversation as a download:
// ?format=md or json (required — the UI resolves the saved preference).
func (s *server) handleExportSession(w http.ResponseWriter, r *http.Request) {
	format := contracts.ExportFormat(r.URL.Query().Get("format"))
	if format != contracts.ExportFormatMarkdown && format != contracts.ExportFormatJSON {
		var v contracts.ValidationError
		v.Add("format", "must be one of: md, json")
		writeError(w, &v)
		return
	}
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	doc, err := s.buildExport(r.Context(), sess, time.Now().UTC())
	if err != nil {
		writeError(w, err)
		return
	}
	var body []byte
	contentType := "text/markdown; charset=utf-8"
	if format == contracts.ExportFormatJSON {
		body, err = renderExportJSON(doc)
		contentType = "application/json"
	} else {
		body = renderExportMarkdown(doc)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", exportFilename(sess.Name, doc.ExportedAt, format)))
	_, _ = w.Write(body)
}

// buildExport gathers the session's transcript and its delegates' — the same
// reads the transcript endpoints do, called directly rather than over HTTP.
func (s *server) buildExport(ctx context.Context, sess contracts.Session, now time.Time) (exportDoc, error) {
	doc := exportDoc{
		Schema:     exportSchema,
		ExportedAt: now,
		Session:    exportSession{ID: sess.ID, Name: sess.Name, Provider: sess.Provider, Model: sess.Model, CreatedAt: sess.CreatedAt},
		Messages:   s.exportMessages(ctx, sess),
		Delegates:  []exportDelegate{},
	}
	list, err := s.data.SessionDelegates(ctx, sess.ID)
	if err != nil {
		return doc, err
	}
	for _, d := range list {
		name := d.Description
		if name == "" {
			name = d.Type
		}
		doc.Delegates = append(doc.Delegates, exportDelegate{
			ID:       d.HarnessID,
			Name:     name,
			Type:     d.Type,
			Messages: s.delegateTranscriptFor(ctx, sess, d.HarnessID).Messages,
		})
	}
	return doc, nil
}

// exportMessages is the session's transcript as GET /sessions/{id}/transcript
// serves it: a running session's child holds the live state, so ask it first
// (the unpaged read, not the browser's page param); otherwise, or if the child
// doesn't answer, the durable read (preferLive).
func (s *server) exportMessages(ctx context.Context, sess contracts.Session) []contracts.TranscriptMessage {
	stored := func() []contracts.TranscriptMessage { return s.transcriptFor(ctx, sess, false) }
	addr, ok := s.sup.SessionAddr(sess.ID)
	if !ok {
		return stored()
	}
	msgs, err := fetchChildTranscript(ctx, addr)
	if err != nil {
		log.Printf("api: export: live transcript for session %s: %v; using stored", sess.ID, err)
	}
	return preferLive(msgs, err, stored)
}

// preferLive picks the child's live transcript unless it failed or came back
// empty. Empty isn't authoritative: a child whose Start hasn't finished —
// a session being woken — answers 200 [] (runner.Transcript has no ref to
// read yet, and says nothing more specific), and exporting that would hand
// the user an empty file for a session with a whole conversation on disk.
// A session that truly has no messages reads empty from the store too.
func preferLive(live []contracts.TranscriptMessage, err error, stored func() []contracts.TranscriptMessage) []contracts.TranscriptMessage {
	if err == nil && len(live) > 0 {
		return live
	}
	return stored()
}

func fetchChildTranscript(ctx context.Context, addr string) ([]contracts.TranscriptMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/transcript", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("child answered %s", resp.Status)
	}
	var msgs []contracts.TranscriptMessage
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, err
	}
	if msgs == nil {
		msgs = []contracts.TranscriptMessage{}
	}
	return msgs, nil
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// exportFilename is <session-name>_<YYYY-MM-DD>.<ext> with the name reduced
// to filename-safe characters (runs of anything else become one "-").
func exportFilename(name string, at time.Time, format contracts.ExportFormat) string {
	base := strings.Trim(unsafeFilenameChars.ReplaceAllString(name, "-"), "-.")
	if base == "" {
		base = "session"
	}
	if len(base) > 80 {
		base = strings.TrimRight(base[:80], "-.")
	}
	return fmt.Sprintf("%s_%s.%s", base, at.Format("2006-01-02"), format)
}

func renderExportJSON(doc exportDoc) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderExportMarkdown renders the export for reading: one heading per
// message, tool calls and results folded in <details>, delegates after the
// main conversation.
func renderExportMarkdown(doc exportDoc) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s\n\n", doc.Session.Name)
	meta := []string{}
	if doc.Session.Provider != "" {
		meta = append(meta, "Provider: "+doc.Session.Provider)
	}
	if doc.Session.Model != "" {
		meta = append(meta, "Model: "+doc.Session.Model)
	}
	meta = append(meta, "Created: "+doc.Session.CreatedAt.Format(time.RFC3339), "Exported: "+doc.ExportedAt.Format(time.RFC3339))
	for _, m := range meta {
		fmt.Fprintf(&b, "- %s\n", m)
	}
	b.WriteString("\n")
	writeMarkdownMessages(&b, doc.Messages, "##")
	for _, d := range doc.Delegates {
		fmt.Fprintf(&b, "---\n\n## Subagent: %s\n\n", d.Name)
		if len(d.Messages) == 0 {
			b.WriteString("_No transcript available._\n\n")
			continue
		}
		writeMarkdownMessages(&b, d.Messages, "###")
	}
	return b.Bytes()
}

func writeMarkdownMessages(w io.Writer, msgs []contracts.TranscriptMessage, h string) {
	for _, m := range msgs {
		stamp := ""
		if !m.Timestamp.IsZero() {
			stamp = " · " + m.Timestamp.UTC().Format(time.RFC3339)
		}
		switch {
		case m.Role == "tool" && m.Tool != nil:
			label := m.Tool.Title
			if label == "" {
				label = m.Tool.Name
			}
			_, _ = fmt.Fprintf(w, "<details><summary>Tool: %s</summary>\n\n", htmlEscape(label))
			if len(m.Tool.Input) > 0 {
				_, _ = fmt.Fprintf(w, "%s\n\n", fence("json", prettyJSON(m.Tool.Input)))
			}
			_, _ = fmt.Fprint(w, "</details>\n\n")
		case m.Role == "tool_result":
			label := "Result"
			if m.IsError {
				label = "Result (error)"
			}
			_, _ = fmt.Fprintf(w, "<details><summary>%s</summary>\n\n%s\n\n</details>\n\n", label, fence("", m.Text))
		case m.Role == "compact":
			_, _ = fmt.Fprintf(w, "%s Compaction%s\n\n", h, stamp)
			if m.Compact != nil {
				_, _ = fmt.Fprintf(w, "_%s: %d → %d tokens_\n\n", m.Compact.Trigger, m.Compact.PreTokens, m.Compact.PostTokens)
			}
			_, _ = fmt.Fprintf(w, "<details><summary>Summary carried forward</summary>\n\n%s\n\n</details>\n\n", m.Text)
		default:
			title := map[string]string{"user": "User", "agent": "Agent", "notification": "System"}[m.Role]
			if title == "" {
				title = m.Role
			}
			if m.IsError && m.Role == "agent" {
				title += " (error)"
			}
			if m.Attribution != nil {
				title = "Message from " + m.Attribution.SourceName
			}
			_, _ = fmt.Fprintf(w, "%s %s%s\n\n%s\n\n", h, title, stamp, m.Text)
		}
	}
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// fence wraps s in a code fence long enough that backticks inside s can't
// close it early.
func fence(lang, s string) string {
	ticks := "```"
	for strings.Contains(s, ticks) {
		ticks += "`"
	}
	return ticks + lang + "\n" + strings.TrimRight(s, "\n") + "\n" + ticks
}
