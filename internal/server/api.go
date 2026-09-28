package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"justswap/internal/core"
)

// Keep-alive pings. Anything under the idle timeouts of common proxies; a long
// enough gap that the stream is never quiet for more than 25s.
const pingGap = 25 * time.Second

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func bad(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"code": strconv.Itoa(code), "message": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes)).Decode(v)
}

func (s *Srv) meta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.c.Meta())
}

func (s *Srv) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"files": s.c.Files()})
}

func (s *Srv) upload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		bad(w, http.StatusBadRequest, "missing file name")
		return
	}
	n, size, err := s.st.Put(name, r.Body)
	if err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	s.c.Add(n, size)
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":  n,
		"bytes": size,
		"url":   "/api/download?name=" + url.QueryEscape(n),
	})
}

func (s *Srv) download(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		bad(w, http.StatusBadRequest, "missing file name")
		return
	}
	f, err := s.st.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			bad(w, http.StatusNotFound, "no such file")
			return
		}
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		bad(w, http.StatusInternalServerError, err.Error())
		return
	}
	size := st.Size()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition(name))
	w.Header().Set("Accept-Ranges", "bytes")

	if rng := r.Header.Get("Range"); rng != "" {
		start, length, ok := parseRange(rng, size)
		if !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			bad(w, http.StatusRequestedRangeNotSatisfiable, "invalid range")
			return
		}
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			bad(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.CopyN(w, f, length)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

func (s *Srv) remove(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		bad(w, http.StatusBadRequest, "missing file name")
		return
	}
	if err := s.st.Remove(name); err != nil {
		bad(w, http.StatusBadRequest, err.Error())
		return
	}
	s.c.Remove(name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Srv) clear(w http.ResponseWriter, r *http.Request) {
	if err := s.st.Wipe(); err != nil {
		bad(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"removed": s.c.Clear()})
}

// settings takes pointers so a partial body only changes what it names.
func (s *Srv) settings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RetentionSeconds *int  `json:"retentionSeconds"`
		ClearOnShutdown  *bool `json:"clearOnShutdown"`
	}
	if err := decode(r, &req); err != nil {
		bad(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.RetentionSeconds != nil {
		sec := *req.RetentionSeconds
		if sec < 1 {
			sec = 1
		}
		const capSec = 366 * 24 * 3600
		if sec > capSec {
			sec = capSec
		}
		s.c.SetRetain(time.Duration(sec) * time.Second)
	}
	if req.ClearOnShutdown != nil {
		s.c.SetClearOnShutdown(*req.ClearOnShutdown)
	}
	writeJSON(w, http.StatusOK, s.c.Meta())
}

func (s *Srv) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		bad(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch := s.c.Events.Subscribe()
	defer s.c.Events.Unsubscribe(ch)

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-cache, no-transform")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	emit := func(ev core.Event) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
		fl.Flush()
	}

	emit(core.Event{Type: "hello", Data: s.c.Meta()})
	emit(core.Event{Type: "files", Data: s.c.Files()})

	t := time.NewTicker(pingGap)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			emit(ev)
		case <-t.C:
			_, _ = io.WriteString(w, ":ping\n\n")
			fl.Flush()
		}
	}
}

// disposition builds an RFC 6266 header. The plain filename is ASCII-only so
// naive clients survive; filename* carries the real name for browsers.
func disposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x80 && r != '"' && r != '\\' {
			return r
		}
		return '_'
	}, name)
	return "attachment; filename=\"" + ascii + "\"; filename*=UTF-8''" + url.QueryEscape(name)
}

// parseRange handles "bytes=A-B" and suffix ranges "bytes=-N". Only one range
// is honoured; multipart byteranges are out of scope.
func parseRange(rng string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(rng, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(rng, "bytes="), ",", 2)[0])
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, 0, false
	}
	if a == "" {
		n, err := strconv.ParseInt(strings.TrimSpace(b), 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, n, true
	}
	start, err := strconv.ParseInt(strings.TrimSpace(a), 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end := size - 1
	if b != "" {
		end, err = strconv.ParseInt(strings.TrimSpace(b), 10, 64)
		if err != nil || end < start {
			return 0, 0, false
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end - start + 1, true
}
