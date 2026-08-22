package controlplane

import (
	"context"
	"log"
	"time"
)

// advanceSession walks the session lifecycle after a new assistant turn has
// been persisted: a draft becomes committed once it has accumulated two
// assistant turns, at which point a background job asks the model for a
// title. It reports whether this call committed the session, which the
// handler surfaces to the UI so the sidebar can refresh.
func (a *App) advanceSession(ctx context.Context, studentID, sessionID string) bool {
	n, err := a.CountMessages(ctx, sessionID, "assistant")
	if err != nil || n != 2 {
		return false
	}
	ses, err := a.Session(ctx, sessionID)
	if err != nil || ses == nil || ses.Committed {
		return false
	}
	if err := a.CommitSession(ctx, sessionID); err != nil {
		return false
	}
	go a.titleSession(studentID, sessionID)
	return true
}

// titleSession asks the model for a short title for a freshly committed
// session and persists it. The call is budgeted like any student interaction;
// a failure leaves the session untitled (the user can rename it).
func (a *App) titleSession(studentID, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msgs, err := a.Messages(ctx, sessionID)
	if err != nil {
		log.Printf("title: failed to load messages for %s: %v", sessionID, err)
		return
	}
	resp, err := a.client.titleFor(ctx, msgs)
	if err != nil {
		log.Printf("title: generation failed for %s: %v", sessionID, err)
		return
	}
	if resp.Text == "" {
		return
	}
	if err := a.RenameSession(ctx, sessionID, resp.Text); err != nil {
		log.Printf("title: failed to persist title for %s: %v", sessionID, err)
		return
	}
	if _, err := a.RecordInteraction(ctx, studentID, sessionID, a.cfg.LocksModel,
		Tokens{Input: resp.Usage.Input, Output: resp.Usage.Output, CacheRead: resp.Usage.CacheRead}); err != nil {
		log.Printf("title: failed to record usage for %s/%s: %v", studentID, sessionID, err)
	}
}
