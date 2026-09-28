// Package core is the single source of truth for the running state.
//
// File metadata lives here in memory and nowhere else — by design, it is not
// persisted. The consequence is that the download directory must be wiped on
// every start, since anything left on disk has no index. Retention, however,
// is a setting and does survive a restart.
package core

import (
	"sort"
	"sync"
	"time"
)

const product = "justswap"

// FileInfo is the whole of the in-memory index: three fields, nothing more.
type FileInfo struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploadedAt"`
}

// View is what crosses the process boundary. ExpiresIn is computed here so the
// client never has to reason about the retention window itself. It is seconds,
// not time.Duration, because a Duration serialises as nanoseconds.
type View struct {
	FileInfo
	ExpiresIn int `json:"expiresIn"`
}

// Meta is the /api/meta payload: server identity plus live settings.
type Meta struct {
	Product          string `json:"product"`
	Version          string `json:"version"`
	Alias            string `json:"alias"`
	BaseDir          string `json:"baseDir"`
	RetentionSeconds int    `json:"retentionSeconds"`
	ClearOnShutdown  bool   `json:"clearOnShutdown"`
	FileCount        int    `json:"fileCount"`
}

type Config struct {
	Version          string
	Alias            string
	BaseDir          string
	RetentionSeconds int
	ClearOnShutdown  bool
}

type Core struct {
	Version string
	Events  *Bus

	// OnConfig is fired after a setting changes so the caller can persist it.
	// It runs without Core.mu held, and must not call back into Core.
	OnConfig func()

	mu        sync.Mutex
	alias     string
	baseDir   string
	retention time.Duration
	clearDown bool
	files     map[string]*FileInfo
}

func New(cfg Config) *Core {
	r := time.Duration(cfg.RetentionSeconds) * time.Second
	if r <= 0 {
		r = time.Hour
	}
	return &Core{
		Version:   cfg.Version,
		Events:    NewBus(),
		alias:     cfg.Alias,
		baseDir:   cfg.BaseDir,
		retention: r,
		clearDown: cfg.ClearOnShutdown,
		files:     map[string]*FileInfo{},
	}
}

func (c *Core) Alias() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.alias
}

func (c *Core) BaseDir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.baseDir
}

func (c *Core) Retain() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retention
}

func (c *Core) ClearOnShutdown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clearDown
}

func (c *Core) SetClearOnShutdown(b bool) {
	c.mu.Lock()
	c.clearDown = b
	fn := c.OnConfig
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
	c.publishSettings()
}

// SetRetain changes the window for every file at once, because expiry is
// derived from UploadedAt plus the current window rather than stored per file.
func (c *Core) SetRetain(d time.Duration) {
	if d <= 0 {
		d = time.Second
	}
	c.mu.Lock()
	c.retention = d
	fn := c.OnConfig
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
	c.publishFiles()
	c.publishSettings()
}

func (c *Core) Add(name string, size int64) *FileInfo {
	f := &FileInfo{Name: name, Size: size, UploadedAt: time.Now()}
	c.mu.Lock()
	c.files[name] = f
	c.mu.Unlock()
	c.publishFiles()
	return f
}

func (c *Core) Remove(name string) bool {
	c.mu.Lock()
	_, ok := c.files[name]
	delete(c.files, name)
	c.mu.Unlock()
	if ok {
		c.publishFiles()
	}
	return ok
}

func (c *Core) Clear() int {
	c.mu.Lock()
	n := len(c.files)
	c.files = map[string]*FileInfo{}
	c.mu.Unlock()
	if n > 0 {
		c.publishFiles()
	}
	return n
}

// PurgeExpired returns the names whose window has elapsed, in no particular
// order. The caller deletes the bytes; core only owns the index.
func (c *Core) PurgeExpired() []string {
	c.mu.Lock()
	var dead []string
	for name, f := range c.files {
		if time.Since(f.UploadedAt) > c.retention {
			delete(c.files, name)
			dead = append(dead, name)
		}
	}
	c.mu.Unlock()
	if len(dead) > 0 {
		c.publishFiles()
	}
	return dead
}

// Files returns the index newest-first. Expired files are still listed with
// ExpiresIn 0 rather than being hidden: housekeeping is what removes them, and
// the UI wants to show them as expired, not make them vanish.
func (c *Core) Files() []View {
	c.mu.Lock()
	now := time.Now()
	out := make([]View, 0, len(c.files))
	for _, f := range c.files {
		left := f.UploadedAt.Add(c.retention).Sub(now)
		if left < 0 {
			left = 0
		}
		out = append(out, View{FileInfo: *f, ExpiresIn: int(left.Seconds())})
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UploadedAt.After(out[j].UploadedAt) })
	return out
}

func (c *Core) Meta() Meta {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Meta{
		Product:          product,
		Version:          c.Version,
		Alias:            c.alias,
		BaseDir:          c.baseDir,
		RetentionSeconds: int(c.retention.Seconds()),
		ClearOnShutdown:  c.clearDown,
		FileCount:        len(c.files),
	}
}

func (c *Core) publishFiles() {
	c.Events.Publish(Event{Type: "files", Data: c.Files()})
}

func (c *Core) publishSettings() {
	c.Events.Publish(Event{Type: "settings", Data: c.Meta()})
}
