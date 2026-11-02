// Command mydiary-server stores encrypted diaries so that several computers
// can keep them in sync. It speaks plain HTTP and is meant to run behind a
// TLS reverse proxy, reachable only from a private network.
//
// Several users can share a server: each device token belongs to a user, and
// a user sees only their own diaries.
//
// Usage:
//
//	mydiary-server [-data DIR] serve [-listen ADDR] [-max-size MiB] [-web DIR]
//	mydiary-server [-data DIR] token add USER DEVICE
//	mydiary-server [-data DIR] token list [USER]
//	mydiary-server [-data DIR] token revoke USER DEVICE
//	mydiary-server [-data DIR] user list
//	mydiary-server [-data DIR] user delete USER
//	mydiary-server [-data DIR] migrate USER
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
	"slices"
	"syscall"
	"time"

	"github.com/oupsman/mydiary/internal/syncserver"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  mydiary-server [-data DIR] serve [-listen ADDR] [-max-size MiB] [-web DIR]
  mydiary-server [-data DIR] token add USER DEVICE   create a device token (printed once)
  mydiary-server [-data DIR] token list [USER]
  mydiary-server [-data DIR] token revoke USER DEVICE
  mydiary-server [-data DIR] user list
  mydiary-server [-data DIR] user delete USER        revoke all its tokens and delete its diaries
  mydiary-server [-data DIR] migrate USER            give a single-user server's diaries and tokens to USER

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
	diaries := filepath.Join(*data, "diaries")

	switch {
	case args[0] == "serve":
		serve(*data, tokens, args[1:])
	case args[0] == "token" && len(args) == 4 && args[1] == "add":
		tok, err := tokens.Add(args[2], args[3])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Token for %s's %q (shown only once, paste it in myDiary's sync settings):\n%s\n", args[2], args[3], tok)
	case args[0] == "token" && (len(args) == 2 || len(args) == 3) && args[1] == "list":
		ds, err := tokens.List()
		if err != nil {
			log.Fatal(err)
		}
		for _, d := range ds {
			if len(args) == 3 && d.User != args[2] {
				continue
			}
			user := d.User
			if user == "" {
				user = "(none: run migrate)"
			}
			fmt.Printf("%-20s %-24s created %s\n", user, d.Name, d.Created.Local().Format(time.DateTime))
		}
	case args[0] == "token" && len(args) == 4 && args[1] == "revoke":
		if err := tokens.Revoke(args[2], args[3]); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Revoked %s's %q.\n", args[2], args[3])
	case args[0] == "user" && len(args) == 2 && args[1] == "list":
		listUsers(diaries, tokens)
	case args[0] == "user" && len(args) == 3 && args[1] == "delete":
		deleteUser(diaries, tokens, args[2])
	case args[0] == "migrate" && len(args) == 2:
		if err := syncserver.Migrate(diaries, tokens, args[1]); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Existing diaries and device tokens now belong to %q.\n", args[1])
	default:
		usage()
		os.Exit(2)
	}
}

// listUsers prints the users that have tokens or diaries.
func listUsers(diaries string, tokens *syncserver.Tokens) {
	users, err := syncserver.Users(diaries)
	if err != nil {
		log.Fatal(err)
	}
	ds, err := tokens.List()
	if err != nil {
		log.Fatal(err)
	}
	count := map[string]int{}
	for _, d := range ds {
		if d.User != "" {
			count[d.User]++
			users = append(users, d.User)
		}
	}
	slices.Sort(users)
	for _, u := range slices.Compact(users) {
		fmt.Printf("%-20s %d device(s)\n", u, count[u])
	}
}

func deleteUser(diaries string, tokens *syncserver.Tokens, user string) {
	if !syncserver.ValidUser(user) {
		log.Fatalf("invalid user name %q", user)
	}
	n, err := tokens.RevokeUser(user)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(diaries, user)); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Revoked %d device(s) and deleted the diaries of %q.\n", n, user)
}

func serve(data string, tokens *syncserver.Tokens, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8484", "address to listen on (keep it private: the reverse proxy does TLS)")
	maxSize := fs.Int64("max-size", 512, "maximum diary size in MiB")
	web := fs.String("web", "", "directory of the web frontend to serve (optional)")
	fs.Parse(args)

	stores, err := syncserver.NewStores(filepath.Join(data, "diaries"), tokens)
	if errors.Is(err, syncserver.ErrLegacyLayout) {
		log.Fatalf("%v: give it to a user first with: mydiary-server -data %s migrate USER", err, data)
	}
	if err != nil {
		log.Fatal(err)
	}
	if ds, _ := tokens.List(); len(ds) == 0 {
		log.Printf("warning: no device tokens yet; create one with: mydiary-server -data %s token add USER DEVICE", data)
	}
	handler := &syncserver.Server{Stores: stores, Tokens: tokens, MaxSize: *maxSize << 20}
	if *web != "" {
		if _, err := os.Stat(filepath.Join(*web, "index.html")); err != nil {
			log.Fatalf("web frontend: %v", err)
		}
		handler.Web = os.DirFS(*web)
		log.Printf("serving the web frontend from %s", *web)
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           handler.Handler(),
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
