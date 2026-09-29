package server

import "fmt"

// Branding describes a built-in, project-wide visual identity. It is applied
// to every landing, participant, and admin page served by one process.
type Branding struct {
	Class     string
	Name      string
	HomeLabel string
}

const (
	BrandingNeutral    = "neutral"
	BrandingPrometheus = "prometheus"
)

func brandingPreset(name string) (Branding, error) {
	switch name {
	case "", BrandingPrometheus:
		return Branding{Class: "brand-prometheus", Name: "Prometheus", HomeLabel: "Prometheus study home"}, nil
	case BrandingNeutral:
		return Branding{Class: "brand-neutral", Name: "Tree study", HomeLabel: "Tree study home"}, nil
	default:
		return Branding{}, fmt.Errorf("unknown branding preset %q; use %q or %q", name, BrandingPrometheus, BrandingNeutral)
	}
}
