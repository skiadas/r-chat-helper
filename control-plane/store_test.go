package controlplane

import (
	"context"
	"testing"
)

func TestSessionLifecycle(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if err := app.AddStudent(ctx, "alice", "alice@college.edu", "Alice", 0); err != nil {
		t.Fatal(err)
	}

	ses, err := app.CreateSession(ctx, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if ses.Committed {
		t.Fatal("new session should start as a draft")
	}

	// Drafts are not listed, but are the student's latest.
	list, err := app.ListSessions(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("draft session listed; got %d sessions", len(list))
	}
	latest, err := app.LatestSession(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != ses.ID {
		t.Fatalf("LatestSession = %+v, want draft %s", latest, ses.ID)
	}

	// Commit makes it visible.
	if err := app.CommitSession(ctx, ses.ID); err != nil {
		t.Fatal(err)
	}
	list, err = app.ListSessions(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != ses.ID {
		t.Fatalf("committed session not listed: %+v", list)
	}

	// Rename persists.
	if err := app.RenameSession(ctx, ses.ID, "geom_point"); err != nil {
		t.Fatal(err)
	}
	if got, err := app.Session(ctx, ses.ID); err != nil || got.Title != "geom_point" {
		t.Fatalf("after rename: %+v, err %v", got, err)
	}

	// Soft delete: gone from list and sessionOwned, but row + messages remain.
	if err := app.AddMessage(ctx, ses.ID, "user", "hi", ""); err != nil {
		t.Fatal(err)
	}
	if err := app.DeleteSession(ctx, ses.ID); err != nil {
		t.Fatal(err)
	}
	if app.sessionOwned(ctx, ses.ID, "alice") {
		t.Fatal("deleted session still owned")
	}
	list, err = app.ListSessions(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("deleted session listed: %+v", list)
	}
	if latest, err := app.LatestSession(ctx, "alice"); err != nil || latest != nil {
		t.Fatalf("LatestSession after delete = %+v, err %v; want none", latest, err)
	}
	if msgs, err := app.Messages(ctx, ses.ID); err != nil || len(msgs) != 1 {
		t.Fatalf("deleted session messages = %+v, err %v; want the row kept", msgs, err)
	}
}

func TestLatestSessionPrefersMostRecentActivity(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if err := app.AddStudent(ctx, "bob", "bob@college.edu", "Bob", 0); err != nil {
		t.Fatal(err)
	}
	a, err := app.CreateSession(ctx, "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.CreateSession(ctx, "bob", ""); err != nil {
		t.Fatal(err)
	}
	// Activity on `a` bumps a.updated_at past the second session's creation.
	if err := app.AddMessage(ctx, a.ID, "user", "hello", ""); err != nil {
		t.Fatal(err)
	}
	latest, err := app.LatestSession(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != a.ID {
		t.Fatalf("LatestSession = %+v, want the recently-active %s", latest, a.ID)
	}
}
