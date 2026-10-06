package attachments

import "strings"

// MessageHeader introduces the attachment list AppendToMessage adds to a
// user message. It's addressed to the agent, and it's also the marker the
// UI (ui/src/conversation/attachmentRefs.ts) looks for to show those lines
// as thumbnails rather than text — change both together.
const MessageHeader = "Attached files (open each one with your file-reading tool):"

// AppendToMessage adds the attachments' paths to the user's text, after it,
// one per line. A message that is only attachments is just the list.
func AppendToMessage(text string, files []Attachment) string {
	if len(files) == 0 {
		return text
	}
	var b strings.Builder
	if t := strings.TrimRight(text, " \t\n"); t != "" {
		b.WriteString(t)
		b.WriteString("\n\n")
	}
	b.WriteString(MessageHeader)
	for _, f := range files {
		b.WriteString("\n- ")
		b.WriteString(f.Path)
	}
	return b.String()
}
