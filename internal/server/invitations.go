package server

import (
	"net/http"
	"net/url"
	"time"
)

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		ExpiresInHours int `json:"expires_in_hours"`
	}
	if err := decodeLimit(w, r, &req, 8<<10); err != nil {
		return err
	}
	if req.ExpiresInHours == 0 {
		req.ExpiresInHours = 168
	}
	if req.ExpiresInHours < 1 || req.ExpiresInHours > 24*30 {
		return problem(422, "expires_in_hours must be between 1 and 720")
	}
	run, err := loadRun(r.Context(), s.db, "id", r.PathValue("id"))
	if err != nil {
		return err
	}
	if run.Recruitment != "invitation" {
		return problem(409, "this run uses public enrollment, not invitations")
	}
	if run.Status == "suspended" {
		return problem(409, "participant writes are suspended")
	}
	token := randomID()
	expires := time.Now().UTC().Add(time.Duration(req.ExpiresInHours) * time.Hour).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(r.Context(), "INSERT INTO invitations(id,run_id,token_hash,created_at,expires_at) VALUES(?,?,?,?,?)", randomID(), run.ID, digest(token), now(), expires); err != nil {
		return err
	}
	base := s.origin
	if base == "" {
		base = ""
	}
	inviteURL := base + "/s/" + url.PathEscape(run.Slug) + "#invite=" + token
	s.audit(r, adminFromContext(r).Username, "invitation.create", "run:"+run.ID, "success")
	return respond(w, map[string]string{"invite_url": inviteURL, "expires_at": expires})
}
