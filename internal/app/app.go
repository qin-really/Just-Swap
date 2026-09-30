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
	"path/filepath"
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
		log.Printf("启动清理 %s: %v", cfg.BaseDir, err)
	}

	c := core.New(core.Config{
		Version:          cfg.Version,
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

	srv := server.New(c, st)
	srv.SetLocalIPs(localIPs())

	mux := http.NewServeMux()
	srv.Register(mux, webui.FS)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go housekeep(ctx, c, st)

	port, ln, err := findAvailablePort(cfg.Listen, cfg.Port)
	if err != nil {
		return err
	}
	cfg.Port = port

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
		log.Println("正在关闭")
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

// findAvailablePort 尝试从 preferred 开始递增查找可用端口,最多尝试 maxTry 次。
// 返回实际使用的端口和监听器。
func findAvailablePort(listen string, preferred int) (int, net.Listener, error) {
	const maxTry = 10
	for i := 0; i < maxTry; i++ {
		port := preferred + i
		addr := net.JoinHostPort(listen, strconv.Itoa(port))
		ln, err := net.Listen("tcp4", addr)
		if err == nil {
			if i > 0 {
				log.Printf("端口 %d 被占用,改用 %d", preferred, port)
			}
			return port, ln, nil
		}
		if i == 0 {
			log.Printf("端口 %d 被占用,尝试 %d..%d", preferred, preferred+1, preferred+maxTry-1)
		}
	}
	return 0, nil, fmt.Errorf("无可用端口: %d-%d", preferred, preferred+maxTry-1)
}

// cleanupOnExit is the one destructive default in the app, so it is logged
// either way: what happened, and that the user can opt out.
func cleanupOnExit(c *core.Core, st *store.Store) {
	if !c.ClearOnShutdown() {
		log.Printf("保留文件于 %s", c.BaseDir())
		return
	}
	if err := st.Wipe(); err != nil {
		log.Printf("关闭清空失败 %s: %v", c.BaseDir(), err)
		return
	}
	log.Printf("已清空 %s 的文件(用 --clear-on-shutdown=false 可关闭此行为)", c.BaseDir())
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
					log.Printf("删除过期文件 %s: %v", name, err)
				}
			}
		}
	}
}

// localIPs returns the machine's usable unicast addresses: every interface
// address except loopback, link-local, multicast, and unspecified. Work loops
// (172.16/12, 10/8, VIPs) count — they are still addresses of this machine.
func localIPs() []net.IP {
	var out []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		out = append(out, ip)
	}
	return out
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

// absDir renders BaseDir as a full path so the banner shows where files land,
// regardless of whether the path came in relative (--download-dir) or from the
// saved settings. Falls back to the raw value if the CWD can't be resolved.
func absDir(dir string) string {
	if p, err := filepath.Abs(dir); err == nil {
		return p
	}
	return dir
}

func printBanner(cfg Config, url string) {
	fmt.Println()
	fmt.Printf("  JustSwap %s - 局域网文件传输\n", cfg.Version)
	fmt.Printf("  %-10s %s\n", "地址", net.JoinHostPort(displayHost(cfg.Listen), strconv.Itoa(cfg.Port)))
	fmt.Printf("  %-10s %s\n", "保存目录", absDir(cfg.BaseDir))
	fmt.Printf("  %-10s %s\n", "保留时长", (time.Duration(cfg.RetentionSeconds) * time.Second).Round(time.Second))
	if cfg.ClearOnShutdown {
		fmt.Printf("  %-10s %s\n", "关闭时", "清空文件")
	} else {
		fmt.Printf("  %-10s %s\n", "关闭时", "保留文件")
	}
	fmt.Println()
	fmt.Println("  使用说明:")
	fmt.Println("  1. 把上面的地址分享给局域网内其他设备")
	fmt.Println("  2. 对方在浏览器打开,拖拽文件即可上传")
	fmt.Println("  3. 文件在保留期后自动删除,或关闭服务器时清空")
	fmt.Println()
	fmt.Println("  无认证:局域网内任何设备均可上传、浏览、下载、删除。")
	fmt.Println("  请仅在可信网络环境下使用,不要暴露到公网。")
	fmt.Println()
	fmt.Println("  按 Ctrl+C 退出")
	fmt.Println()
}
