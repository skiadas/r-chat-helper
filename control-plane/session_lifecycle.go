package controlplane

import (
	"context"
	"errors"
	"log"
	"time"
)

// Sentinel errors for session-boundary operations, mapped to HTTP statuses by
// the handlers that call them.
var (
	ErrSessionNotFound   = errors.New("session not found")
	ErrAlreadySummarized = errors.New("session already created from a summary")
)

// acceptsTurn is the session size gate: a send onto a session whose last
// completed turn's prompt tokens reached maxTokens is refused, unless the cap
// is disabled (maxTokens <= 0). summaryAvailable reports whether the student
// could still continue from a summary of this session; it is meaningful only
// when the turn is refused, and is false for sessions already created from a
// summary (a summary composes at most once).
func acceptsTurn(ses *Session, maxTokens int64) (allowed, summaryAvailable bool) {
	if maxTokens <= 0 || ses == nil || ses.LastPromptTokens < maxTokens {
		return true, false
	}
	return false, !ses.HasSummary
}

// startFromSummary creates a fresh session seeded with a lazily generated
// summary of the source conversation, so the student can continue a session
// past its size cap without a cold start. The summary is generated only here
// (the offer never pays for an unused summary); a failed generation falls back
// to a cold start so the flow never dead-ends. The generated summary is priced
// like any interaction, charged to the source session. Returns ErrNotFound for
// a foreign or missing source and ErrAlreadySummarized for a source that was
// itself created from a summary (once-only rule).
func (a *App) startFromSummary(ctx context.Context, studentID, sourceID string) (*Session, bool, error) {
	src, err := a.Session(ctx, sourceID)
	if err != nil {
		return nil, false, err
	}
	if src == nil || src.Deleted || src.StudentID != studentID {
		return nil, false, ErrSessionNotFound
	}
	if src.HasSummary {
		return nil, false, ErrAlreadySummarized
	}
	msgs, err := a.Messages(ctx, sourceID)
	if err != nil {
		return nil, false, err
	}
	summary := ""
	if len(msgs) > 0 {
		genCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		resp, err := a.client.summaryFor(genCtx, msgs)
		cancel()
		if err != nil {
			log.Printf("summary: generation failed for %s: %v", sourceID, err)
		} else if resp != nil && resp.Text != "" {
			summary = resp.Text
			if _, err := a.RecordInteraction(context.Background(), studentID, sourceID, a.cfg.LocksModel,
				Tokens{Input: resp.Usage.Input, Output: resp.Usage.Output, CacheRead: resp.Usage.CacheRead}); err != nil {
				log.Printf("summary: failed to record usage for %s/%s: %v", studentID, sourceID, err)
			}
		}
	}
	if summary != "" {
		ses, err := a.CreateSessionWithSummary(ctx, studentID, "", summary)
		return ses, true, err
	}
	ses, err := a.CreateSession(ctx, studentID, "")
	return ses, false, err
}

// startFromTopic creates a fresh session seeded with the source's last
// exchange, so a student following the tutor's new-topic suggestion keeps the
// immediate thread without the older cruft. Only the student-visible text is
// carried; fetched tool output stays with the source session. No summary is
// carried: a topic break starts clean.
func (a *App) startFromTopic(ctx context.Context, studentID, sourceID string) (*Session, error) {
	if !a.sessionOwned(ctx, sourceID, studentID) {
		return nil, ErrSessionNotFound
	}
	msgs, err := a.Messages(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if len(msgs) > 2 {
		msgs = msgs[len(msgs)-2:]
	}
	ses, err := a.CreateSession(ctx, studentID, "")
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if err := a.AddMessage(ctx, ses.ID, m.Role, m.Text, ""); err != nil {
			return nil, err
		}
	}
	return ses, nil
}

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
