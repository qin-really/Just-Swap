// Package app is the wiring layer: it resolves settings, builds the pieces,
// and owns the process lifecycle. It contains no request handling.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"justswap/internal/core"
	"justswap/internal/server"
	"justswap/internal/store"
	"justswap/internal/webui"
)

const (
	housekeepingEvery = 30 * time.Second
	shutdownTimeout   = 5 * time.Second
)

type Config struct {
	Version          string
	Port             int
	Listen           string
	Alias            string
	BaseDir          string
	RetentionSeconds int
	ClearOnShutdown  bool
	NoBrowser        bool
}

func Run(cfg Config) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return fmt.Errorf("invalid port %d", cfg.Port)
	}
	if cfg.Alias == "" {
		cfg.Alias = "JustSwap"
	}
	// 默认存当前目录下的 data/：随二进制走，不占 C 盘。
	if cfg.BaseDir == "" {
		cfg.BaseDir = "data"
	}
	if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", cfg.BaseDir, err)
	}

	st, err := store.New(cfg.BaseDir)
	if err != nil {
		return err
	}
	// Anything already on disk is an orphan: file metadata is deliberately not
	// persisted, so there is no way to tell what a leftover file is. Clearing
	// it at start is what makes "metadata lives in memory" safe.
	if err := st.Wipe(); err != nil {
		log.Printf("startup cleanup of %s: %v", cfg.BaseDir, err)
	}

	c := core.New(core.Config{
		Version:          cfg.Version,
		Alias:            cfg.Alias,
		BaseDir:          cfg.BaseDir,
		RetentionSeconds: cfg.RetentionSeconds,
		ClearOnShutdown:  cfg.ClearOnShutdown,
	})
	// Only settings change through the API; flags set at launch are never
	// written back, so a one-off --download-dir does not become a preference.
	c.OnConfig = func() {
		_ = SaveSettings(Settings{
			Alias:            c.Alias(),
			BaseDir:          cfg.BaseDir,
			RetentionSeconds: int(c.Retain().Seconds()),
			ClearOnShutdown:  c.ClearOnShutdown(),
		})
	}

	mux := http.NewServeMux()
	server.New(c, st).Register(mux, webui.FS)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go housekeep(ctx, c, st)

	addr := net.JoinHostPort(cfg.Listen, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w (port in use?)", addr, err)
	}

	hs := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	url := "http://" + net.JoinHostPort(displayHost(cfg.Listen), strconv.Itoa(cfg.Port))
	if !cfg.NoBrowser {
		go openBrowser(url)
	}
	printBanner(cfg, url)

	go func() {
		<-ctx.Done()
		log.Println("shutting down")
		shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = hs.Shutdown(shCtx)
		cleanupOnExit(c, st)
	}()

	err = hs.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// cleanupOnExit is the one destructive default in the app, so it is logged
// either way: what happened, and that the user can opt out.
func cleanupOnExit(c *core.Core, st *store.Store) {
	if !c.ClearOnShutdown() {
		log.Printf("kept files in %s", c.BaseDir())
		return
	}
	if err := st.Wipe(); err != nil {
		log.Printf("clear-on-shutdown failed for %s: %v", c.BaseDir(), err)
		return
	}
	log.Printf("cleared files from %s (disable with --clear-on-shutdown=false)", c.BaseDir())
}

// housekeep drops expired files. It lives here rather than in core because
// core owns the index and store owns the bytes; nobody should make core aware
// of the filesystem just to run a timer.
func housekeep(ctx context.Context, c *core.Core, st *store.Store) {
	t := time.NewTicker(housekeepingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, name := range c.PurgeExpired() {
				if err := st.Remove(name); err != nil {
					log.Printf("remove expired %s: %v", name, err)
				}
			}
		}
	}
}

// displayHost picks the address a peer machine would need to reach us. Dialing
// UDP sends no packets, so this works on an air-gapped LAN.
func displayHost(listen string) string {
	if strings.HasPrefix(listen, "127.") || listen == "localhost" {
		return "127.0.0.1"
	}
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil {
		return a.IP.To4().String()
	}
	return "127.0.0.1"
}

func openBrowser(url string) {
	time.Sleep(400 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func printBanner(cfg Config, url string) {
	fmt.Println()
	fmt.Printf("  JustSwap %s - LAN file sharing\n", cfg.Version)
	fmt.Printf("  %-14s %s\n", "address", url)
	fmt.Printf("  %-14s %s\n", "alias", cfg.Alias)
	fmt.Printf("  %-14s %s\n", "files", cfg.BaseDir)
	fmt.Printf("  %-14s %s\n", "retention", (time.Duration(cfg.RetentionSeconds) * time.Second).Round(time.Second))
	if cfg.ClearOnShutdown {
		fmt.Printf("  %-14s %s\n", "on shutdown", "clears files")
	} else {
		fmt.Printf("  %-14s %s\n", "on shutdown", "keeps files")
	}
	fmt.Println()
	fmt.Println("  No authentication: anyone on the network can upload,")
	fmt.Println("  browse, download, and delete.")
	fmt.Println()
	fmt.Println("  press Ctrl+C to quit")
	fmt.Println()
}
