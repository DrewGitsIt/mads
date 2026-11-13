package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

const (
	// backgroundPollInterval is how often handleBackgroundOutput re-stats the
	// output file: a plain poll-tail, like the pipeline's.
	backgroundPollInterval = 500 * time.Millisecond
	// backgroundTailBytes is how far back from the end a fresh connection
	// starts: a long build log arrives as its last 64 KB, not megabytes.
	backgroundTailBytes = 64 * 1024
	// backgroundChunkBytes caps one frame, so a file that grew a lot between
	// polls goes out in pieces rather than one huge read.
	backgroundChunkBytes = 256 * 1024
	// backgroundEndedTail is how many of the file's last bytes Ended sees.
	backgroundEndedTail = 256
)

// handleBackgroundOutput streams the output of a tool call that kept running
// as a background process (provider.BackgroundOutputReader; Claude Code
// only today), read from the harness's own file — mads keeps no copy. The
// call is named by its tool_use id; the adapter works out and checks the
// file, so no path ever comes from the request.
//
// An SSE stream, always 200 (an EventSource can't read a status):
//
//	event: trimmed  data: <bytes skipped>   the stream starts mid-file
//	id: <offset>    data: <base64 bytes>     output; raw bytes, so a UTF-8
//	                                         character or escape split
//	                                         across chunks survives
//	event: reset    data:                    the file shrank (replaced):
//	                                         clear and start over
//	event: end      data: <reason>           "ended" (the process finished
//	                                         and every byte was sent),
//	                                         "gone" (the file was removed),
//	                                         "none" (no background output for
//	                                         that call, or unsupported)
//
// A reconnect resumes from Last-Event-ID, the offset after the last chunk
// it got. Otherwise the stream starts at the last backgroundTailBytes,
// moved forward to just past a newline so it opens on a whole line. A file
// with no end marker is followed until the client goes away.
func (s *server) handleBackgroundOutput(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	end := func(reason string) {
		if _, err := fmt.Fprintf(w, "event: end\ndata: %s\n\n", reason); err == nil {
			flusher.Flush()
		}
	}
	out, err := s.openBackgroundOutput(r, sess.Provider, delegateRefs(sess), r.PathValue("toolUseId"))
	switch {
	case errors.Is(err, provider.ErrBackgroundOutputGone):
		end("gone")
		return
	case err != nil:
		end("none")
		return
	}
	f := out.File
	defer func() { _ = f.Close() }()

	// start opens the file from its tail, telling the client how much it
	// skipped — and, when the client held output from another file, to drop it.
	start := func(size int64, reset bool) (int64, error) {
		if reset {
			if _, err := fmt.Fprint(w, "event: reset\ndata:\n\n"); err != nil {
				return 0, err
			}
		}
		from := tailStart(f, size)
		if from > 0 {
			if _, err := fmt.Fprintf(w, "event: trimmed\ndata: %d\n\n", from); err != nil {
				return 0, err
			}
		}
		return from, nil
	}
	info, err := f.Stat()
	if err != nil {
		return
	}
	offset, resumed := lastEventOffset(r)
	if !resumed || offset > info.Size() {
		if offset, err = start(info.Size(), resumed); err != nil {
			return
		}
	}

	ctx := r.Context()
	ticker := time.NewTicker(backgroundPollInterval)
	defer ticker.Stop()
	for {
		info, err := f.Stat()
		if err != nil {
			return
		}
		size := info.Size()
		if size < offset {
			// Shrunk under us: not the output this stream was following.
			if offset, err = start(size, true); err != nil {
				return
			}
		}
		for offset < size {
			n := min(size-offset, backgroundChunkBytes)
			buf := make([]byte, n)
			if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
				return
			}
			offset += n
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", offset, base64.StdEncoding.EncodeToString(buf)); err != nil {
				return
			}
		}
		flusher.Flush()
		if out.Ended != nil && out.Ended(lastBytes(f, size, backgroundEndedTail)) {
			end("ended")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// openBackgroundOutput asks the session's provider for the call's output
// under each of the session's refs (an import continued since keeps its
// earlier calls under the original's), the first answer that isn't "none"
// winning.
func (s *server) openBackgroundOutput(r *http.Request, providerName string, refs []string, toolUseID string) (*provider.BackgroundOutput, error) {
	p, err := s.providers.Get(providerName)
	if err != nil {
		return nil, provider.ErrNoBackgroundOutput
	}
	reader, ok := p.(provider.BackgroundOutputReader)
	if !ok {
		return nil, provider.ErrNoBackgroundOutput
	}
	for _, ref := range refs {
		out, err := reader.OpenBackgroundOutput(r.Context(), ref, toolUseID)
		if errors.Is(err, provider.ErrNoBackgroundOutput) {
			continue
		}
		if err != nil && !errors.Is(err, provider.ErrBackgroundOutputGone) {
			log.Printf("api: background output: %s %s: %v", ref, toolUseID, err)
		}
		return out, err
	}
	return nil, provider.ErrNoBackgroundOutput
}

// lastEventOffset reads a reconnecting EventSource's Last-Event-ID.
func lastEventOffset(r *http.Request) (int64, bool) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// tailStart is where a fresh stream of a size-byte file begins: the last
// backgroundTailBytes, moved just past the first newline in them so the
// view opens on a whole line (and never inside a UTF-8 character or escape
// sequence). With no newline there, the raw cut stands.
func tailStart(f io.ReaderAt, size int64) int64 {
	if size <= backgroundTailBytes {
		return 0
	}
	start := size - backgroundTailBytes
	buf := make([]byte, backgroundTailBytes)
	n, _ := f.ReadAt(buf, start)
	if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
		return start + int64(i) + 1
	}
	return start
}

// lastBytes is up to n of the file's final bytes.
func lastBytes(f io.ReaderAt, size int64, n int64) []byte {
	start := max(size-n, 0)
	buf := make([]byte, size-start)
	m, _ := f.ReadAt(buf, start)
	return buf[:m]
}
