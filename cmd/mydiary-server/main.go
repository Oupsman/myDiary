// Command mydiary-server stores encrypted diaries so that several computers
// can keep them in sync. It speaks plain HTTP and is meant to run behind a
// TLS reverse proxy, reachable only from a private network.
//
// Usage:
//
//	mydiary-server [-data DIR] serve [-listen ADDR] [-max-size MiB]
//	mydiary-server [-data DIR] token add NAME
//	mydiary-server [-data DIR] token list
//	mydiary-server [-data DIR] token revoke NAME
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oupsman/mydiary/internal/syncserver"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  mydiary-server [-data DIR] serve [-listen ADDR] [-max-size MiB]
  mydiary-server [-data DIR] token add NAME     create a device token (printed once)
  mydiary-server [-data DIR] token list
  mydiary-server [-data DIR] token revoke NAME

Global flags:
`)
	flag.PrintDefaults()
}

func main() {
	data := flag.String("data", "/var/lib/mydiary", "data directory")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	if err := os.MkdirAll(*data, 0o700); err != nil {
		log.Fatal(err)
	}
	tokens := syncserver.NewTokens(filepath.Join(*data, "tokens.json"))

	switch {
	case args[0] == "serve":
		serve(*data, tokens, args[1:])
	case args[0] == "token" && len(args) == 3 && args[1] == "add":
		tok, err := tokens.Add(args[2])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Token for %q (shown only once, paste it in myDiary's sync settings):\n%s\n", args[2], tok)
	case args[0] == "token" && len(args) == 2 && args[1] == "list":
		ds, err := tokens.List()
		if err != nil {
			log.Fatal(err)
		}
		for _, d := range ds {
			fmt.Printf("%-24s created %s\n", d.Name, d.Created.Local().Format(time.DateTime))
		}
	case args[0] == "token" && len(args) == 3 && args[1] == "revoke":
		if err := tokens.Revoke(args[2]); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Revoked %q.\n", args[2])
	default:
		usage()
		os.Exit(2)
	}
}

func serve(data string, tokens *syncserver.Tokens, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8484", "address to listen on (keep it private: the reverse proxy does TLS)")
	maxSize := fs.Int64("max-size", 512, "maximum diary size in MiB")
	fs.Parse(args)

	store, err := syncserver.NewStore(filepath.Join(data, "diaries"))
	if err != nil {
		log.Fatal(err)
	}
	if ds, _ := tokens.List(); len(ds) == 0 {
		log.Printf("warning: no device tokens yet; create one with: mydiary-server -data %s token add NAME", data)
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           (&syncserver.Server{Store: store, Tokens: tokens, MaxSize: *maxSize << 20}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("mydiary-server listening on %s, data in %s", *listen, data)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
