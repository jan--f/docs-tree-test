// Package server implements the persistent tree-test HTTP API.
package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Server struct {
	db     *sql.DB
	assets fs.FS
	origin string
	secure bool
	mux    *http.ServeMux
}

type apiError struct {
	status int
	text   string
}

func (e *apiError) Error() string           { return e.text }
func problem(status int, text string) error { return &apiError{status, text} }

type handler func(http.ResponseWriter, *http.Request) error

func (s *Server) route(pattern string, h handler) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.sameOrigin(r) {
				writeError(w, problem(http.StatusForbidden, "same-origin request required"))
				return
			}
		}
		if err := h(w, r); err != nil {
			writeError(w, err)
		}
	})
}

func writeError(w http.ResponseWriter, err error) {
	var e *apiError
	if !errors.As(err, &e) {
		log.Printf("tree-test API: %v", err)
		e = &apiError{http.StatusInternalServerError, "internal server error"}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": e.text})
}

func respond(w http.ResponseWriter, value any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(value)
}

func rawResponse(w http.ResponseWriter, data []byte) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write(data)
	return err
}

func decode(w http.ResponseWriter, r *http.Request, value any) error {
	if media := strings.Split(r.Header.Get("Content-Type"), ";")[0]; media != "application/json" {
		return problem(http.StatusUnsupportedMediaType, "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return problem(http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return problem(http.StatusBadRequest, "request must contain one JSON object")
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func marshal(value any) ([]byte, error) { return json.Marshal(value) }

func (s *Server) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	want := s.origin
	if want == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		want = scheme + "://" + r.Host
	}
	if got := r.Header.Get("Origin"); got != "" {
		return got == want
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && u.Scheme+"://"+u.Host == want
	}
	return false
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Cache-Control", "no-store")
	if s.secure {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux = http.NewServeMux()
	s.route("GET /api/public/{slug}", s.publicInfo)
	s.route("POST /api/public/{slug}/join", s.join)
	s.route("GET /api/public/{slug}/session", s.session)
	s.route("POST /api/public/{slug}/start", s.start)
	s.route("POST /api/public/{slug}/events", s.events)
	s.route("POST /api/public/{slug}/finish", s.finish)
	s.route("GET /admin/api/session", s.adminSession)
	s.route("POST /admin/api/login", s.login)
	s.route("POST /admin/api/logout", s.authorize(false, s.logout))
	s.route("POST /admin/api/validate", s.authorize(true, s.validate))
	s.route("GET /admin/api/drafts", s.authorize(true, s.listDrafts))
	s.route("POST /admin/api/drafts", s.authorize(true, s.createDraft))
	s.route("GET /admin/api/drafts/{id}", s.authorize(true, s.getDraft))
	s.route("PUT /admin/api/drafts/{id}", s.authorize(true, s.updateDraft))
	s.route("POST /admin/api/drafts/{id}/publish", s.authorize(true, s.publish))
	s.route("GET /admin/api/versions", s.authorize(true, s.listVersions))
	s.route("GET /admin/api/runs", s.authorize(false, s.listRuns))
	s.route("POST /admin/api/runs", s.authorize(true, s.createRun))
	s.route("PATCH /admin/api/runs/{id}", s.authorize(true, s.updateRun))
	s.route("GET /admin/api/runs/{id}/results", s.authorize(false, s.results))
	s.route("GET /admin/api/runs/{id}/export", s.authorize(false, s.export))
	s.route("GET /admin/api/runs/{id}/key", s.authorize(true, s.key))
	s.route("GET /admin/api/runs/{id}/events", s.authorize(true, s.eventExport))
	s.route("GET /{$}", s.html("index.html"))
	s.route("GET /admin", s.html("admin.html"))
	s.route("GET /s/{slug}", s.html("participant.html"))
	s.mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, problem(405, "method not allowed"))
			return
		}
		if s.assets == nil {
			writeError(w, problem(404, "asset not found"))
			return
		}
		// Asset filenames are stable across releases; revalidate on reload so a
		// new protocol is never paired with a five-minute-old browser controller.
		w.Header().Set("Cache-Control", "no-cache")
		http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
	})
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, problem(404, "not found")) })
}

func (s *Server) html(name string) handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if s.assets == nil {
			return problem(404, "frontend unavailable")
		}
		data, err := fs.ReadFile(s.assets, name)
		if err != nil {
			return fmt.Errorf("read frontend %s: %w", name, err)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return nil
		}
		_, err = w.Write(data)
		return err
	}
}
