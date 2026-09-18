package search

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrHistoryBudget = errors.New("accepted search history budget is not an explicit bounded pair")

// acceptedHistorySeedKey is an unexported context key type; only this package
// can place a seed into a request context.
type acceptedHistorySeedKey struct{}

const (
	// RootHistoryBudget bounds how many previously accepted product turns may
	// enter one model prompt and how many bytes they may occupy. The budget is
	// never implicit: the product boundary passes exactly one value.
	rootHistoryMaxTurns = 8
	rootHistoryMinBytes = 256
	rootHistoryMaxBytes = 32 << 10
)

// RootHistoryBudget is the explicit injection budget for accepted search
// turns. Zero values keep the boundary's original no-injection behavior.
type RootHistoryBudget struct {
	MaxTurns int `json:"max_turns"`
	MaxBytes int `json:"max_bytes"`
}

func (b RootHistoryBudget) valid() bool {
	return b.MaxTurns >= 1 && b.MaxTurns <= rootHistoryMaxTurns &&
		b.MaxBytes >= rootHistoryMinBytes && b.MaxBytes <= rootHistoryMaxBytes
}

// Valid reports whether the budget is one explicit bounded pair. Zero values
// mean the deployment disabled accepted-history injection.
func (b RootHistoryBudget) Valid() bool { return b.valid() }

// acceptedHistoryCitation is one verified fact reference from an accepted
// turn: the RTW-accepted evidence identity and its exact quote bytes.
type acceptedHistoryCitation struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote"`
}

// acceptedHistoryTurn carries only the accepted product facts of one prior
// turn. Raw framework events, rejected candidates and private attempt records
// have no representation here.
type acceptedHistoryTurn struct {
	SearchID  string                    `json:"search_id"`
	AnswerID  string                    `json:"answer_id"`
	Question  string                    `json:"question"`
	Answer    string                    `json:"answer"`
	Citations []acceptedHistoryCitation `json:"verified_citations"`
}

// acceptedHistorySeed is the immutable block handed to the root Graph through
// the request context. Only the RootSessionBoundary — the owner of the
// accepted history contract — constructs it.
type acceptedHistorySeed struct {
	Budget RootHistoryBudget     `json:"budget"`
	Turns  []acceptedHistoryTurn `json:"turns"`
	Block  string                `json:"block"`
}

func WithAcceptedHistorySeed(ctx context.Context, seed acceptedHistorySeed) context.Context {
	return context.WithValue(ctx, acceptedHistorySeedKey{}, seed)
}

func acceptedHistorySeedFromContext(ctx context.Context) (acceptedHistorySeed, bool) {
	if ctx == nil {
		return acceptedHistorySeed{}, false
	}
	seed, ok := ctx.Value(acceptedHistorySeedKey{}).(acceptedHistorySeed)
	return seed, ok
}

// acceptedHistoryTurnOf projects one validated accepted turn onto its injectable
// facts. A turn without an answer (e.g. "insufficient") carries no injectable
// facts and reports ok=false; it is skipped, not treated as corruption. A
// "succeeded" turn must cite only evidence that exists in its own accepted pack
// with nonempty quotes — anything else is a corrupted history and fails closed.
func acceptedHistoryTurnOf(turn AcceptedRootTurn) (acceptedHistoryTurn, bool, error) {
	if turn.Result.SummaryStatus != "succeeded" {
		if turn.Result.Answer != "" || len(turn.Result.Citations) != 0 {
			return acceptedHistoryTurn{}, false, ErrAcceptedHistory
		}
		return acceptedHistoryTurn{}, false, nil
	}
	if turn.Result.Answer == "" {
		return acceptedHistoryTurn{}, false, ErrAcceptedHistory
	}
	quotes := make(map[string]string, len(turn.Result.Search.Pack.Evidence))
	for _, evidence := range turn.Result.Search.Pack.Evidence {
		if evidence.ID == "" || evidence.Quote == "" {
			return acceptedHistoryTurn{}, false, ErrAcceptedHistory
		}
		quotes[evidence.ID] = evidence.Quote
	}
	out := acceptedHistoryTurn{SearchID: turn.Request.SearchID, AnswerID: turn.Result.AnswerID,
		Question: turn.Request.Search.Query, Answer: turn.Result.Answer}
	seen := make(map[string]bool, len(turn.Result.Citations))
	for _, id := range turn.Result.Citations {
		quote, ok := quotes[id]
		if !ok || seen[id] {
			return acceptedHistoryTurn{}, false, ErrAcceptedHistory
		}
		seen[id] = true
		out.Citations = append(out.Citations, acceptedHistoryCitation{EvidenceID: id, Quote: quote})
	}
	if len(out.Citations) == 0 {
		return acceptedHistoryTurn{}, false, ErrAcceptedHistory
	}
	return out, true, nil
}

// renderAcceptedHistoryBlock renders at most budget.MaxTurns newest accepted
// turns into one canonical context block. Oldest turns are dropped first, and a
// whole turn that would not fit is dropped with its predecessors. An empty
// result means the prompt must not carry a history field at all. The raw
// envelope bytes stay available for verification; the caller never re-formats
// them.
func renderAcceptedHistoryBlock(turns []acceptedHistoryTurn, budget RootHistoryBudget) (string, error) {
	if !budget.valid() {
		return "", ErrHistoryBudget
	}
	if len(turns) > budget.MaxTurns {
		turns = turns[len(turns)-budget.MaxTurns:]
	}
	block := ""
	for len(turns) > 0 {
		raw, err := json.Marshal(struct {
			AcceptedSessionHistory []acceptedHistoryTurn `json:"accepted_session_history"`
			HistoryBudget          RootHistoryBudget     `json:"accepted_history_budget"`
		}{turns, budget})
		if err != nil {
			return "", err
		}
		if len(raw) <= budget.MaxBytes {
			block = string(raw)
			break
		}
		turns = turns[1:]
	}
	return block, nil
}

func historySeedIntegrity(seed acceptedHistorySeed) error {
	if !seed.Budget.valid() {
		return ErrHistoryBudget
	}
	var prefix string
	if len(seed.Turns) > 0 {
		raw, err := json.Marshal(struct {
			AcceptedSessionHistory []acceptedHistoryTurn `json:"accepted_session_history"`
			HistoryBudget          RootHistoryBudget     `json:"accepted_history_budget"`
		}{seed.Turns, seed.Budget})
		if err != nil {
			return err
		}
		prefix = string(raw)
	}
	if prefix != seed.Block {
		return ErrAcceptedHistory
	}
	if len(seed.Block) > seed.Budget.MaxBytes {
		return ErrHistoryBudget
	}
	return nil
}
