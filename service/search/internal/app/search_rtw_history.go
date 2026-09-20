package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/ridethewind"
	btwruntime "github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/runtime/httpclient"
	searchdomain "github.com/Sea-Go/Sea-BreakTheWaves/service/search/internal/search"
)

var ErrRTWAcceptedHistory = errors.New("RTW accepted answer history differs from fixed product turn")

type AcceptedHistoryClient interface {
	CommitAcceptedAnswer(context.Context, ridethewind.CommitAcceptedAnswerReq) (ridethewind.AcceptedAnswer, error)
	GetAcceptedAnswer(context.Context, ridethewind.GetAcceptedAnswerReq) (ridethewind.AcceptedAnswer, error)
	ListAcceptedAnswers(context.Context, ridethewind.ListAcceptedAnswersReq) (ridethewind.AcceptedAnswersPage, error)
}

// RTWAcceptedRootHistory maps only validated product turns to RTW's durable
// answer projection. It does not persist or replay the framework Agent Session.
type RTWAcceptedRootHistory struct{ client AcceptedHistoryClient }

func NewRTWAcceptedRootHistory(client AcceptedHistoryClient) (*RTWAcceptedRootHistory, error) {
	if nilDependency(client) {
		return nil, ErrRTWAcceptedHistory
	}
	return &RTWAcceptedRootHistory{client: client}, nil
}

func rtwSubject(s btwruntime.SubjectRef) ridethewind.AcceptedSubjectRef {
	return ridethewind.AcceptedSubjectRef{AuthorityId: s.AuthorityID, TenantId: s.TenantID, SubjectId: s.SubjectID}
}

func (h *RTWAcceptedRootHistory) Commit(ctx context.Context, turn searchdomain.AcceptedRootTurn) error {
	if h == nil || nilDependency(h.client) || ctx == nil || turn.Request.SearchID == "" || turn.Request.AnswerID == "" ||
		turn.Request.SessionID == "" || turn.Result.AnswerID != turn.Request.AnswerID ||
		turn.Result.Search.Pack.SearchID != turn.Request.SearchID {
		return ErrRTWAcceptedHistory
	}
	if _, err := turn.Request.Subject.UserKey(); err != nil {
		return ErrRTWAcceptedHistory
	}
	raw, err := json.Marshal(turn)
	if err != nil {
		return fmt.Errorf("encode validated product turn: %w", err)
	}
	q := ridethewind.CommitAcceptedAnswerReq{AnswerId: turn.Request.AnswerID,
		SearchId: turn.Request.SearchID, Subject: rtwSubject(turn.Request.Subject),
		SessionId: turn.Request.SessionID, TurnJson: string(raw)}
	accepted, err := h.client.CommitAcceptedAnswer(ctx, q)
	if err != nil {
		// The POST response can be lost after RTW commits. Query the exact
		// AnswerID and scope; a different committed turn never fulfills it.
		recovered, readErr := h.client.GetAcceptedAnswer(ctx, ridethewind.GetAcceptedAnswerReq{
			AnswerId: q.AnswerId, AuthorityId: q.Subject.AuthorityId,
			TenantId: q.Subject.TenantId, SubjectId: q.Subject.SubjectId, SessionId: q.SessionId})
		if readErr == nil && sameAcceptedAnswer(recovered, q, turn.Result.SummaryStatus) {
			return nil
		}
		return errors.Join(err, readErr)
	}
	if !sameAcceptedAnswer(accepted, q, turn.Result.SummaryStatus) {
		return ErrRTWAcceptedHistory
	}
	return nil
}

func sameAcceptedAnswer(answer ridethewind.AcceptedAnswer, q ridethewind.CommitAcceptedAnswerReq, status string) bool {
	return answer.AnswerId == q.AnswerId && answer.SearchId == q.SearchId && answer.Subject == q.Subject &&
		answer.SessionId == q.SessionId && answer.TurnJson == q.TurnJson && answer.Status == status &&
		answer.AcceptedOrdinal > 0 && answer.AcceptedAt != ""
}

// Get lets the search boundary replay a previously accepted AnswerID before
// starting another Agent run. Only RTW's scoped 404 means no accepted turn;
// timeouts, malformed envelopes and mismatched receipts remain errors.
func (h *RTWAcceptedRootHistory) Get(ctx context.Context, subject btwruntime.SubjectRef, sessionID, answerID string) (searchdomain.AcceptedRootTurn, bool, error) {
	if h == nil || nilDependency(h.client) || ctx == nil || sessionID == "" || answerID == "" {
		return searchdomain.AcceptedRootTurn{}, false, ErrRTWAcceptedHistory
	}
	if _, err := subject.UserKey(); err != nil {
		return searchdomain.AcceptedRootTurn{}, false, ErrRTWAcceptedHistory
	}
	answer, err := h.client.GetAcceptedAnswer(ctx, ridethewind.GetAcceptedAnswerReq{
		AnswerId: answerID, AuthorityId: subject.AuthorityID, TenantId: subject.TenantID,
		SubjectId: subject.SubjectID, SessionId: sessionID})
	if err != nil {
		var status *httpclient.HTTPError
		if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
			return searchdomain.AcceptedRootTurn{}, false, nil
		}
		return searchdomain.AcceptedRootTurn{}, false, fmt.Errorf("get RTW accepted answer: %w", err)
	}
	if answer.AnswerId != answerID || answer.Subject != rtwSubject(subject) || answer.SessionId != sessionID ||
		answer.SearchId == "" || answer.AcceptedOrdinal < 1 || answer.AcceptedAt == "" ||
		(answer.Status != "succeeded" && answer.Status != "insufficient") {
		return searchdomain.AcceptedRootTurn{}, false, ErrRTWAcceptedHistory
	}
	var turn searchdomain.AcceptedRootTurn
	if err := json.Unmarshal([]byte(answer.TurnJson), &turn); err != nil ||
		turn.Request.Subject != subject || turn.Request.SessionID != sessionID ||
		turn.Request.AnswerID != answerID || turn.Request.SearchID != answer.SearchId ||
		turn.Result.AnswerID != answerID || turn.Result.Search.Pack.SearchID != answer.SearchId ||
		turn.Result.SummaryStatus != answer.Status {
		return searchdomain.AcceptedRootTurn{}, false, ErrRTWAcceptedHistory
	}
	return turn, true, nil
}

// List is capped because AcceptedRootHistory's current interface has no page
// argument. A large history must fail explicitly, never silently truncate.
func (h *RTWAcceptedRootHistory) List(ctx context.Context, subject btwruntime.SubjectRef, sessionID string) ([]searchdomain.AcceptedRootTurn, error) {
	if h == nil || nilDependency(h.client) || ctx == nil || sessionID == "" {
		return nil, ErrRTWAcceptedHistory
	}
	if _, err := subject.UserKey(); err != nil {
		return nil, ErrRTWAcceptedHistory
	}
	const pageSize, maxTurns = 100, 1000
	var after int64
	turns := make([]searchdomain.AcceptedRootTurn, 0)
	for {
		page, err := h.client.ListAcceptedAnswers(ctx, ridethewind.ListAcceptedAnswersReq{
			AuthorityId: subject.AuthorityID, TenantId: subject.TenantID,
			SubjectId: subject.SubjectID, SessionId: sessionID, AfterOrdinal: after, Limit: pageSize})
		if err != nil {
			return nil, fmt.Errorf("list RTW accepted answers: %w", err)
		}
		for _, answer := range page.Items {
			if answer.Subject != rtwSubject(subject) || answer.SessionId != sessionID || answer.AcceptedOrdinal <= after {
				return nil, ErrRTWAcceptedHistory
			}
			var turn searchdomain.AcceptedRootTurn
			if json.Unmarshal([]byte(answer.TurnJson), &turn) != nil ||
				turn.Request.Subject != subject || turn.Request.SessionID != sessionID ||
				turn.Request.AnswerID != answer.AnswerId || turn.Request.SearchID != answer.SearchId ||
				turn.Result.SummaryStatus != answer.Status {
				return nil, ErrRTWAcceptedHistory
			}
			turns = append(turns, turn)
			if len(turns) > maxTurns {
				return nil, ErrRTWAcceptedHistory
			}
			after = answer.AcceptedOrdinal
		}
		if page.NextOrdinal == 0 {
			return turns, nil
		}
		if len(page.Items) == 0 || page.NextOrdinal != after {
			return nil, ErrRTWAcceptedHistory
		}
	}
}

var _ searchdomain.AcceptedRootHistory = (*RTWAcceptedRootHistory)(nil)
var _ searchdomain.AcceptedRootLookup = (*RTWAcceptedRootHistory)(nil)
