package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBrandingPresetsRenderTrustedValues(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":                 {Data: []byte(`<html class="{{branding-class}}"><title>{{branding-name}}</title><a aria-label="{{branding-home-label}}">{{branding-name}}</a>`)},
		"participant.html":           {Data: []byte(`<html class="{{branding-class}}">`)},
		"admin.html":                 {Data: []byte(`<html class="{{branding-class}}">`)},
		"assets/prometheus-logo.svg": {Data: []byte(`<svg/>`)},
	}
	for _, test := range []struct {
		name, preset, wantClass, wantName string
	}{
		{name: "prometheus by default", wantClass: "brand-prometheus", wantName: "Prometheus"},
		{name: "explicit prometheus", preset: BrandingPrometheus, wantClass: "brand-prometheus", wantName: "Prometheus"},
		{name: "neutral fallback", preset: BrandingNeutral, wantClass: "brand-neutral", wantName: "Tree study"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := OpenWithOptions(":memory:", assets, testOrigin, Options{Branding: test.preset})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })

			request := httptest.NewRequest("GET", testOrigin+"/", nil)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != 200 || !strings.Contains(body, `class="`+test.wantClass+`"`) || !strings.Contains(body, test.wantName) || strings.Contains(body, "{{branding-") {
				t.Fatalf("rendered preset %q incorrectly: status=%d body=%q", test.preset, response.Code, body)
			}
		})
	}
}

func TestUnknownBrandingDoesNotOpenDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "study.sqlite")
	s, err := OpenWithOptions(db, testAssets, testOrigin, Options{Branding: "unknown"})
	if err == nil || s != nil {
		t.Fatalf("unknown preset accepted: server=%v err=%v", s, err)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("unknown preset opened database: %v", err)
	}
}
