// treetest serves and administers a single-server documentation tree study.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jan--f/docs-tree-test/internal/server"
	"github.com/jan--f/docs-tree-test/internal/study"
	"github.com/jan--f/docs-tree-test/web"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	if args[0] == "version" {
		fmt.Println("treetest", version)
		return nil
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage()
		return nil
	}
	if args[0] == "identity-key" {
		return generateIdentityKey(args[1:])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	db := flags.String("db", "treetest.sqlite", "SQLite database path (persistent local disk)")
	publicURL := flags.String("public-url", "http://127.0.0.1:8080", "External origin, including http:// or https://")
	branding := flags.String("branding", server.BrandingPrometheus, "Visual branding preset: prometheus or neutral")
	trustedProxies := flags.String("trusted-proxies", "127.0.0.1/32,::1/128", "Comma-separated immediate proxy IPs/CIDRs allowed to set X-Forwarded-For; empty trusts none")
	var listen, studyDir, username, role, passwordFile, out, identityKeyFile, purgeRun *string
	var purgeOverdue, purgeConfirm *bool
	identityKeyFile = flags.String("identity-key-file", "", "File containing a base64url 32-byte participant identity key")
	switch args[0] {
	case "serve":
		listen = flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	case "validate", "import":
		studyDir = flags.String("study", "studies/prometheus", "Directory containing study.json and Markdown trees")
	case "user":
		username = flags.String("username", "owner", "Account name")
		role = flags.String("role", "owner", "owner or analyst")
		passwordFile = flags.String("password-file", "", "Read password from a file; otherwise use TREETEST_PASSWORD or generate one")
	case "backup":
		out = flags.String("out", "", "New SQLite backup file (must not already exist)")
	case "purge":
		purgeRun = flags.String("run", "", "Closed run ID to purge")
		purgeOverdue = flags.Bool("overdue", false, "Select all closed runs past their retention target")
		purgeConfirm = flags.Bool("confirm", false, "Actually delete participant data after the dry run")
	default:
		return fmt.Errorf("unknown command %q; use treetest help", args[0])
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if args[0] == "validate" {
		bundle, err := study.LoadDir(*studyDir)
		if err != nil {
			return err
		}
		snapshot, err := study.Validate(bundle)
		if err != nil {
			return err
		}
		fmt.Printf("Valid study: %s\nSHA-256: %s\nVariants: %d; tasks: %d; panels: %d; tasks/session: %d\n", bundle.Config.Title, snapshot.Hash, len(bundle.Config.Variants), len(bundle.Config.Tasks), len(bundle.Config.Panels), bundle.Config.TasksPerSession)
		return nil
	}
	u, err := url.Parse(*publicURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("public-url must be an HTTP(S) origin without a path, query, credentials or fragment")
	}
	identityKey, err := loadIdentityKey(*identityKeyFile)
	if err != nil {
		return err
	}
	proxies := []string{}
	if strings.TrimSpace(*trustedProxies) != "" {
		proxies = strings.Split(*trustedProxies, ",")
	}
	s, err := server.OpenWithOptions(*db, web.Assets, strings.TrimRight(*publicURL, "/"), server.Options{IdentityKey: identityKey, Branding: *branding, TrustedProxies: proxies})
	if err != nil {
		return err
	}
	defer s.Close()
	switch args[0] {
	case "serve":
		httpServer := &http.Server{
			Addr: *listen, Handler: s, ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
			IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			<-ctx.Done()
			deadline, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(deadline); err != nil {
				log.Printf("HTTP shutdown: %v", err)
			}
		}()
		log.Printf("treetest %s listening on %s (public origin %s)", version, *listen, *publicURL)
		err := httpServer.ListenAndServe()
		stop()
		<-shutdownDone
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case "user":
		password := os.Getenv("TREETEST_PASSWORD")
		if *passwordFile != "" {
			raw, err := os.ReadFile(*passwordFile)
			if err != nil {
				return err
			}
			password = strings.TrimRight(string(raw), "\r\n")
		}
		generated := password == ""
		if generated {
			password = rand.Text()
		}
		if err := s.SetUser(*username, password, *role); err != nil {
			return err
		}
		fmt.Printf("Saved %s account %q.\n", *role, *username)
		if generated {
			fmt.Printf("Generated password: %s\n", password)
		}
	case "import":
		bundle, err := study.LoadDir(*studyDir)
		if err != nil {
			return err
		}
		id, err := s.Import(bundle)
		if err != nil {
			return err
		}
		fmt.Printf("Published version: %s\nCreate a pilot run in /admin to try it.\n", id)
	case "backup":
		if *out == "" {
			return fmt.Errorf("backup requires --out")
		}
		if err := s.Backup(*out); err != nil {
			return err
		}
		fmt.Println("Backup saved:", *out)
	case "purge":
		if (*purgeRun == "" && !*purgeOverdue) || (*purgeRun != "" && *purgeOverdue) {
			return errors.New("purge requires exactly one of --run or --overdue")
		}
		candidates, err := s.PurgeCandidates(context.Background(), *purgeRun, *purgeOverdue)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			fmt.Println("No closed runs match the purge selection.")
			return nil
		}
		for _, candidate := range candidates {
			fmt.Printf("%s (%s): %d sessions, %d attempts, %d events, %d receipts, %d invitations\n", candidate.Slug, candidate.RunID, candidate.Sessions, candidate.Attempts, candidate.Events, candidate.Receipts, candidate.Invitations)
		}
		if !*purgeConfirm {
			fmt.Println("Dry run only. Re-run with --confirm to permanently delete this participant data.")
			return nil
		}
		for _, candidate := range candidates {
			if err := s.PurgeRun(context.Background(), candidate.RunID); err != nil {
				return err
			}
		}
		fmt.Printf("Purged participant data from %d closed run(s).\n", len(candidates))
	}
	return nil
}

// Key generation is independent of the database, so an external key never needs
// to be stored in SQLite, even during initial deployment setup.
func generateIdentityKey(args []string) error {
	flags := flag.NewFlagSet("identity-key", flag.ContinueOnError)
	out := flags.String("out", "", "New private file for a random participant identity key (must not already exist)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *out == "" {
		return errors.New("identity-key requires --out")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(base64.RawURLEncoding.EncodeToString(key) + "\n")
	if err := errors.Join(writeErr, f.Close()); err != nil {
		_ = os.Remove(*out)
		return err
	}
	fmt.Println("New participant identity key saved:", *out)
	return nil
}

func loadIdentityKey(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value := strings.TrimSpace(string(raw))
	key, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("identity-key-file must contain a base64url-encoded 32-byte key")
	}
	return key, nil
}

func usage() {
	fmt.Print(`Documentation tree testing

Usage: treetest <command> [flags]

  serve         Run the participant and admin web application
  user          Create/reset an owner or analyst account (prints a generated password)
  validate      Validate a study directory without opening a database
  import        Publish a validated study directory into the database
  backup        Create a consistent SQLite backup
  identity-key  Generate a new external participant identity key without opening a database
  purge         Dry-run or confirm manual deletion of closed-run participant data
  version       Print the application version

Use treetest <command> -h for command flags. Normal study management is in /admin.
`)
}
