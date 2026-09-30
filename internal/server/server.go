// Package server owns the HTTP surface: route registration and every handler.
// It takes core and store as dependencies and knows nothing about persistence,
// so it can be tested against an in-memory store and a temp directory.
package server

import (
	"embed"
	"net"
	"net/http"

	"justswap/internal/core"
	"justswap/internal/store"
)

// MaxBodyBytes caps JSON request bodies. File uploads are never limited by
// this; they stream straight to disk.
const MaxBodyBytes = 4 << 20

type Srv struct {
	c        *core.Core
	st       *store.Store
	localIPs []net.IP // this machine's interface addresses, for same-machine checks
}

func New(c *core.Core, st *store.Store) *Srv {
	return &Srv{c: c, st: st}
}

// SetLocalIPs records the machine's interface addresses. Used by /api/meta's
// `local` field so the UI can default the QR code to visible only on the
// device running the server. Fixed at startup.
func (s *Srv) SetLocalIPs(ips []net.IP) {
	s.localIPs = ips
}

// Register wires the full route table onto mux.
//
// /api/... is the only surface the browser talks to. There is no separate
// peer namespace any more, since the rebuild dropped device-to-device transfer.
func (s *Srv) Register(mux *http.ServeMux, ui embed.FS) {
	mux.Handle("GET /api/meta", http.HandlerFunc(s.meta))
	mux.Handle("GET /api/files", http.HandlerFunc(s.list))
	mux.Handle("POST /api/upload", http.HandlerFunc(s.upload))
	mux.Handle("GET /api/download", http.HandlerFunc(s.download))
	mux.Handle("DELETE /api/files", http.HandlerFunc(s.remove))
	mux.Handle("POST /api/clear", http.HandlerFunc(s.clear))
	mux.Handle("PATCH /api/settings", http.HandlerFunc(s.settings))
	mux.Handle("GET /api/events", http.HandlerFunc(s.stream))
	mux.Handle("GET /", http.FileServerFS(ui))
}
