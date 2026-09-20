package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/async/internal/usermodel"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/common/clients/datacenter/wire/eventing"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	commentFactProducer = "rtw.comment-rpc"
	likeFactProducer    = "rtw.like-mq"
)

type CommunityAuthorityBinderConfig struct {
	BaseURL  string
	Token    string
	Producer string
	Client   *http.Client
}

// CommunityAuthorityBinder accepts semantic fields only from a source-owned
// RTW authority response whose frozen EventSpec and DC receipt match the batch.
type CommunityAuthorityBinder struct {
	baseURL  string
	token    string
	producer string
	client   *http.Client
}

func NewCommunityAuthorityBinder(cfg CommunityAuthorityBinderConfig) (*CommunityAuthorityBinder, error) {
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		len(cfg.Token) < 32 || (cfg.Producer != commentFactProducer && cfg.Producer != likeFactProducer) {
		return nil, fmt.Errorf("RTW community authority endpoint, producer, and token: %w", ErrFactDeliveryContract)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	if cfg.Client != nil {
		copy := *cfg.Client
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 3 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &CommunityAuthorityBinder{baseURL: parsed.String(), token: cfg.Token, producer: cfg.Producer, client: client}, nil
}

type communityAuthorityResponse struct {
	Event              eventing.Event            `json:"event"`
	SubjectRef         communitySourceSubjectRef `json:"subject_ref"`
	PredecessorEventID string                    `json:"predecessor_event_id,omitempty"`
	TechnicalReceipt   eventing.Receipt          `json:"technical_receipt"`
	SourceEventHash    string                    `json:"source_event_hash"`
}

// communitySourceSubjectRef is the RTW v2 product identity contract. It has no
// tenant or realm selector; the issuer owns the globally scoped subject ID.
type communitySourceSubjectRef struct {
	Issuer    string `json:"issuer"`
	SubjectID string `json:"subject_id"`
}

type communityAuthorityPayload struct {
	SchemaVersion    string  `json:"schema_version"`
	EventID          string  `json:"event_id"`
	EventType        string  `json:"event_type"`
	Producer         string  `json:"producer"`
	AggregateID      string  `json:"aggregate_id"`
	AggregateVersion *int64  `json:"aggregate_version"`
	OperationID      string  `json:"operation_id"`
	SubjectRef       string  `json:"subject_ref"`
	OperatorRef      string  `json:"operator_ref,omitempty"`
	TargetType       string  `json:"target_type"`
	TargetID         string  `json:"target_id"`
	TargetRevision   *string `json:"target_revision"`
	RevisionStatus   string  `json:"revision_status"`
	Operation        string  `json:"operation"`
	SourceRef        string  `json:"source_ref"`
	CommentID        string  `json:"comment_id,omitempty"`
	ParentCommentID  string  `json:"parent_comment_id,omitempty"`
	OldState         *int32  `json:"old_state,omitempty"`
	NewState         *int32  `json:"new_state,omitempty"`
	VisibilityState  *int32  `json:"visibility_state,omitempty"`
	SearchEvidence   *bool   `json:"search_evidence,omitempty"`
	EventTime        string  `json:"event_time"`
	OccurredAt       string  `json:"occurred_at"`
	AvailableAt      string  `json:"available_at"`
}

func (b *CommunityAuthorityBinder) BindFactWithEvidence(ctx context.Context, source FactSourceEvidence) (usermodel.Event, error) {
	if b == nil || source.Event.Producer != b.producer || source.Offset < 1 ||
		!factDeliveryHash.MatchString(source.InputHash) || source.Receipt.Producer != source.Event.Producer ||
		source.Receipt.EventID != source.Event.EventID || source.Receipt.InputHash != source.InputHash ||
		source.Receipt.Offset != source.Offset || source.Receipt.ReceiptID == "" || source.Receipt.TechnicalStatus != "accepted" {
		return usermodel.Event{}, fmt.Errorf("DC community evidence: %w", ErrFactDeliveryContract)
	}
	endpoint := b.baseURL + "/internal/v1/community/facts?producer=" + url.QueryEscape(b.producer) +
		"&event_id=" + url.QueryEscape(source.Event.EventID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return usermodel.Event{}, err
	}
	request.Header.Set("Authorization", "Bearer "+b.token)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	response, err := b.client.Do(request)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("read RTW community authority: %w: %w", ErrFactAuthorityUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return usermodel.Event{}, fmt.Errorf("RTW community authority HTTP %d: %w", response.StatusCode, ErrFactAuthorityUnavailable)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(body) > 256<<10 {
		return usermodel.Event{}, fmt.Errorf("RTW community authority response size: %w", ErrFactDeliveryContract)
	}
	var authority communityAuthorityResponse
	if err := strictCommunityJSON(bytes.NewReader(body), &authority); err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW community authority response: %w", ErrFactDeliveryContract)
	}
	return bindCommunityAuthority(source, authority)
}

func bindCommunityAuthority(source FactSourceEvidence, authority communityAuthorityResponse) (usermodel.Event, error) {
	sourceRaw, err := json.Marshal(source.Event)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("DC community EventSpec: %w", ErrFactDeliveryContract)
	}
	authorityRaw, err := json.Marshal(authority.Event)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW community EventSpec: %w", ErrFactDeliveryContract)
	}
	sourceCanonical, err := jsoncanonicalizer.Transform(sourceRaw)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("DC community EventSpec JCS: %w", ErrFactDeliveryContract)
	}
	authorityCanonical, err := jsoncanonicalizer.Transform(authorityRaw)
	if err != nil || !bytes.Equal(sourceCanonical, authorityCanonical) {
		return usermodel.Event{}, fmt.Errorf("RTW source and DC EventSpec differ: %w", ErrFactDeliveryContract)
	}
	sum := sha256.Sum256(authorityCanonical)
	hash := hex.EncodeToString(sum[:])
	if hash != authority.SourceEventHash || hash != source.InputHash || authority.TechnicalReceipt.InputHash != hash ||
		authority.TechnicalReceipt.EventID != source.Event.EventID ||
		authority.TechnicalReceipt.Producer != source.Event.Producer ||
		authority.TechnicalReceipt.TechnicalStatus != "accepted" ||
		authority.TechnicalReceipt.ReceiptID != source.Receipt.ReceiptID ||
		authority.TechnicalReceipt.Offset != source.Offset {
		return usermodel.Event{}, fmt.Errorf("RTW/DC community hash or receipt differs: %w", ErrFactDeliveryContract)
	}
	rtwReceived, err := time.Parse(time.RFC3339Nano, authority.TechnicalReceipt.ReceivedAt)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW community receipt time: %w", ErrFactDeliveryContract)
	}
	dcReceived, err := time.Parse(time.RFC3339Nano, source.Receipt.ReceivedAt)
	if err != nil || !rtwReceived.Equal(dcReceived) {
		return usermodel.Event{}, fmt.Errorf("RTW/DC community receipt time differs: %w", ErrFactDeliveryContract)
	}
	var payload communityAuthorityPayload
	if err := strictCommunityJSON(bytes.NewReader(authority.Event.Payload), &payload); err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW community payload shape: %w", ErrFactDeliveryContract)
	}
	if err := validateCommunityPayload(source.Event, authority, payload); err != nil {
		return usermodel.Event{}, err
	}
	return communitySemanticFact(source.Event, authority, payload)
}

func validateCommunityPayload(event eventing.Event, authority communityAuthorityResponse, payload communityAuthorityPayload) error {
	expectedSubject := "rtw.identity/platform/" + authority.SubjectRef.SubjectID
	uid, uidErr := strconv.ParseInt(authority.SubjectRef.SubjectID, 10, 64)
	if authority.SubjectRef.Issuer != "rtw.identity" ||
		uidErr != nil || uid <= 0 || strconv.FormatInt(uid, 10) != authority.SubjectRef.SubjectID ||
		payload.SubjectRef != expectedSubject || payload.SchemaVersion != "rtw.community-fact.v1" ||
		payload.EventID != event.EventID || payload.EventType != event.EventType || payload.Producer != event.Producer ||
		payload.AggregateVersion != nil || payload.OperationID != event.OperationID ||
		payload.TargetType == "" || payload.TargetID == "" || payload.TargetRevision != nil ||
		payload.RevisionStatus != "unknown" || payload.EventTime != event.OccurredAt || payload.OccurredAt != event.OccurredAt {
		return fmt.Errorf("RTW community source fields: %w", ErrFactDeliveryContract)
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.AvailableAt); err != nil {
		return fmt.Errorf("RTW community availability time: %w", ErrFactDeliveryContract)
	}
	if !factDeliveryToken.MatchString(payload.TargetType) || !factDeliveryToken.MatchString(payload.TargetID) ||
		!factDeliveryToken.MatchString(payload.OperationID) {
		return fmt.Errorf("RTW community bounded source references: %w", ErrFactDeliveryContract)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &raw); err != nil || !isJSONNull(raw["target_revision"]) ||
		!isJSONNull(raw["aggregate_version"]) {
		return fmt.Errorf("RTW community unresolved revision fields: %w", ErrFactDeliveryContract)
	}
	return nil
}

func communitySemanticFact(event eventing.Event, authority communityAuthorityResponse, payload communityAuthorityPayload) (usermodel.Event, error) {
	// usermodel.SubjectRef still names its historical namespace compatibility
	// slot TenantID. This adapter writes the constant "platform" there; no
	// tenant or realm is accepted or exposed by the v2 authority contract.
	fact := usermodel.Event{Subject: usermodel.SubjectRef{AuthorityID: authority.SubjectRef.Issuer,
		TenantID: "platform", SubjectID: authority.SubjectRef.SubjectID},
		Kind: usermodel.ProductAction, ItemID: payload.TargetID}
	switch event.Producer {
	case commentFactProducer:
		if payload.CommentID == "" || event.AggregateID != payload.CommentID || payload.VisibilityState == nil ||
			payload.SearchEvidence == nil || *payload.SearchEvidence || payload.AggregateID != payload.CommentID {
			return usermodel.Event{}, fmt.Errorf("RTW comment authority fields: %w", ErrFactDeliveryContract)
		}
		commentID, err := strconv.ParseInt(payload.CommentID, 10, 64)
		if err != nil || commentID <= 0 || strconv.FormatInt(commentID, 10) != payload.CommentID {
			return usermodel.Event{}, fmt.Errorf("RTW comment identifier: %w", ErrFactDeliveryContract)
		}
		fact.ValueRef = "comment/" + payload.CommentID
		sourceRef := "rtw.comment/" + payload.CommentID
		switch event.EventType {
		case "community.comment.created":
			if payload.Operation != "create" || payload.SourceRef != sourceRef || event.EventID != "rtw.comment."+payload.CommentID+".created" ||
				authority.PredecessorEventID != "" || payload.OldState != nil || payload.NewState != nil {
				return usermodel.Event{}, fmt.Errorf("RTW comment create semantics: %w", ErrFactDeliveryContract)
			}
			fact.Action, fact.Predicate = usermodel.Assert, "comment"
		case "community.comment.deleted":
			if payload.Operation != "retract" || payload.SourceRef != sourceRef || event.EventID != "rtw.comment."+payload.CommentID+".deleted" ||
				authority.PredecessorEventID != "rtw.comment."+payload.CommentID+".created" || payload.OldState != nil || payload.NewState != nil {
				return usermodel.Event{}, fmt.Errorf("RTW comment delete semantics: %w", ErrFactDeliveryContract)
			}
			fact.Action, fact.Predicate = usermodel.Retract, "comment"
			fact.Supersedes = &usermodel.EventKey{Producer: event.Producer, EventID: authority.PredecessorEventID}
		case "community.comment.interaction":
			if payload.SourceRef != event.EventID || !strings.HasPrefix(event.EventID, "rtw.comment.interaction.") {
				return usermodel.Event{}, fmt.Errorf("RTW comment interaction identity: %w", ErrFactDeliveryContract)
			}
			var err error
			fact.Action, fact.Predicate, fact.Supersedes, err = transitionSemantic(event.Producer, authority.PredecessorEventID,
				payload.Operation, payload.OldState, payload.NewState, "comment_like", "comment_dislike")
			if err != nil {
				return usermodel.Event{}, err
			}
		default:
			return usermodel.Event{}, fmt.Errorf("RTW comment event type: %w", ErrFactDeliveryContract)
		}
	case likeFactProducer:
		if event.EventType != "community.target.interaction" || payload.CommentID != "" || payload.VisibilityState != nil ||
			payload.SearchEvidence != nil || payload.AggregateID != payload.TargetType+"/"+payload.TargetID ||
			event.AggregateID != "like-state/"+authority.SubjectRef.SubjectID+"/"+payload.TargetType+"/"+payload.TargetID ||
			payload.EventID != "rtw.like."+payload.OperationID || payload.SourceRef != payload.EventID {
			return usermodel.Event{}, fmt.Errorf("RTW target reaction authority fields: %w", ErrFactDeliveryContract)
		}
		var err error
		fact.Action, fact.Predicate, fact.Supersedes, err = transitionSemantic(event.Producer, authority.PredecessorEventID,
			payload.Operation, payload.OldState, payload.NewState, "like", "dislike")
		if err != nil {
			return usermodel.Event{}, err
		}
		fact.ValueRef = payload.TargetType + "/" + payload.TargetID
	default:
		return usermodel.Event{}, fmt.Errorf("RTW community producer: %w", ErrFactDeliveryContract)
	}
	return fact, nil
}

func transitionSemantic(producer, predecessor, operation string, oldState, newState *int32,
	positivePredicate, negativePredicate string) (usermodel.Action, string, *usermodel.EventKey, error) {
	if oldState == nil || newState == nil || *oldState == *newState || *oldState < 0 || *oldState > 2 || *newState < 0 || *newState > 2 {
		return "", "", nil, fmt.Errorf("RTW reaction state transition: %w", ErrFactDeliveryContract)
	}
	predicate := positivePredicate
	if *newState == 2 || (*newState == 0 && *oldState == 2) {
		predicate = negativePredicate
	}
	validOperation := map[string]bool{
		"like": *newState == 1 && *oldState != 1, "unlike": *oldState == 1 && *newState == 0,
		"dislike": *newState == 2 && *oldState != 2, "undislike": *oldState == 2 && *newState == 0,
	}[operation]
	if !validOperation {
		return "", "", nil, fmt.Errorf("RTW reaction operation: %w", ErrFactDeliveryContract)
	}
	if *oldState == 0 {
		if predecessor != "" {
			return "", "", nil, fmt.Errorf("RTW reaction assertion predecessor: %w", ErrFactDeliveryContract)
		}
		return usermodel.Assert, predicate, nil, nil
	}
	if !factDeliveryToken.MatchString(predecessor) {
		return "", "", nil, fmt.Errorf("RTW reaction predecessor: %w", ErrFactDeliveryContract)
	}
	key := &usermodel.EventKey{Producer: producer, EventID: predecessor}
	if *newState == 0 {
		return usermodel.Retract, predicate, key, nil
	}
	return usermodel.Correct, predicate, key, nil
}

func isJSONNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func strictCommunityJSON(reader io.Reader, out any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing community authority JSON")
	}
	return nil
}
