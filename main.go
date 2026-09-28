package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/user"

	"justswap/internal/app"
)

// version is the app version, reported by GET /api/meta.
const version = "0.1.0"

func main() {
	log.SetFlags(0)
	saved := app.LoadSettings()

	// 默认存当前目录下的 data/：随二进制走，不占 C 盘。
	base := saved.BaseDir
	if base == "" {
		base = "data"
	}

	port := flag.Int("port", 8787, "tcp port to listen on")
	listen := flag.String("listen", "0.0.0.0", "address to bind")
	alias := flag.String("alias", defaultAlias(saved.Alias), "server name shown in the browser")
	dl := flag.String("download-dir", base, "directory uploaded files are kept in")
	ret := flag.Int("retention", saved.RetentionSeconds, "seconds a file is kept before deletion")
	clean := flag.Bool("clear-on-shutdown", saved.ClearOnShutdown, "delete every file when the server stops")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser on start")
	showVer := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVer {
		fmt.Println("justswap", version)
		return
	}

	// Flag defaults already carry the saved values, so *ret and *clean are
	// correct whether or not the user overrode them.
	if err := app.Run(app.Config{
		Version:          version,
		Port:             *port,
		Listen:           *listen,
		Alias:            *alias,
		BaseDir:          *dl,
		RetentionSeconds: *ret,
		ClearOnShutdown:  *clean,
		NoBrowser:        *noBrowser,
	}); err != nil {
		log.Fatal(err)
	}
}

func defaultAlias(saved string) string {
	if saved != "" {
		return saved
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "JustSwap"
}
