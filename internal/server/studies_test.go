package server

import (
	"context"
	"strings"
	"testing"
)

func TestPublishedStudyRemoval(t *testing.T) {
	s, err := Open(":memory:", testAssets, testOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if studies, err := s.ListStudies(ctx); err != nil || len(studies) != 0 {
		t.Fatalf("empty list: %+v %v", studies, err)
	}
	first, err := s.Import(fixture())
	if err != nil {
		t.Fatal(err)
	}
	changed := fixture()
	changed.Config.Title = "Other study"
	second, err := s.Import(changed)
	if err != nil {
		t.Fatal(err)
	}
	studies, err := s.ListStudies(ctx)
	if err != nil || len(studies) != 2 {
		t.Fatalf("list: %+v %v", studies, err)
	}
	info, err := s.GetStudy(ctx, first)
	if err != nil || info.Slug != "source-study" || info.Title != "Find your way" || info.Hash == "" || info.Runs != 0 {
		t.Fatalf("get: %+v %v", info, err)
	}
	if err := s.RemoveStudy(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing version: %v", err)
	}
	if _, err := s.db.Exec("DELETE FROM versions WHERE id=?", first); err == nil {
		t.Fatal("normal deletes bypassed the immutability trigger")
	}
	if err := s.RemoveStudy(ctx, first); err != nil {
		t.Fatal(err)
	}
	if count(t, s, "SELECT COUNT(*) FROM versions WHERE id=?", first) != 0 || count(t, s, "SELECT COUNT(*) FROM versions WHERE id=?", second) != 1 {
		t.Fatal("removal deleted the wrong version")
	}
	if _, err := s.db.Exec("DELETE FROM versions WHERE id=?", second); err == nil {
		t.Fatal("removal left the immutability trigger disabled")
	}
	newID, err := s.Import(fixture())
	if err != nil || newID == first {
		t.Fatalf("reimport after removal: %q %v", newID, err)
	}
	if err := s.SetUser("owner", "long-test-password", "owner"); err != nil {
		t.Fatal(err)
	}
	owner := loginClient(t, s, "owner")
	run := call[run](t, owner, "POST", "/admin/api/runs", map[string]string{"version_id": newID, "slug": "used-study", "mode": "pilot"}, 200)
	if err := s.RemoveStudy(ctx, newID); err == nil || !strings.Contains(err.Error(), "1 run(s)") {
		t.Fatalf("used version removal: %v", err)
	}
	info, err = s.GetStudy(ctx, newID)
	if err != nil || info.Runs != 1 || count(t, s, "SELECT COUNT(*) FROM runs WHERE id=?", run.ID) != 1 {
		t.Fatalf("used version changed: %+v %v", info, err)
	}
	if _, err := s.db.Exec("DELETE FROM versions WHERE id=?", second); err == nil {
		t.Fatal("failed removal left the immutability trigger disabled")
	}
}
