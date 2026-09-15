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
	"regexp"
	"strconv"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/clients/datacenter/wire/eventing"
	"github.com/Sea-Go/Sea-BreakTheWaves/internal/usermodel"
	jsoncanonicalizer "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const favoriteFactProducer = "rtw.community.favorite"
const maxLegacyExactJSONID int64 = 9007199254740991

var ErrFactAuthorityUnavailable = errors.New("RTW favorite fact authority unavailable")
var favoriteEventID = regexp.MustCompile(`^favorite\.([1-9][0-9]*)\.v([12])$`)

type FavoriteAuthorityBinderConfig struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// FavoriteAuthorityBinder is opt-in: constructor rejects an absent RTW
// authority endpoint or service token. The bound subject and predecessor come
// from RTW's frozen source record, never from the DC transport payload.
type FavoriteAuthorityBinder struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewFavoriteAuthorityBinder(cfg FavoriteAuthorityBinderConfig) (*FavoriteAuthorityBinder, error) {
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		len(cfg.Token) < 32 {
		return nil, fmt.Errorf("RTW favorite authority endpoint and service token: %w", ErrFactDeliveryContract)
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
	return &FavoriteAuthorityBinder{baseURL: parsed.String(), token: cfg.Token, client: client}, nil
}

type favoriteAuthorityResponse struct {
	Event              json.RawMessage  `json:"event"`
	SubjectRef         json.RawMessage  `json:"subject_ref"`
	PredecessorEventID string           `json:"predecessor_event_id,omitempty"`
	TechnicalReceipt   eventing.Receipt `json:"technical_receipt"`
	SourceEventHash    string           `json:"source_event_hash"`
}

type favoriteAuthorityPayload struct {
	SchemaVersion  int             `json:"schema_version"`
	EventID        string          `json:"event_id"`
	SubjectRef     json.RawMessage `json:"subject_ref"`
	TargetType     string          `json:"target_type"`
	TargetID       string          `json:"target_id"`
	TargetRevision *string         `json:"target_revision"`
	Operation      string          `json:"operation"`
	SourceRef      string          `json:"source_ref"`
	EventTime      string          `json:"event_time"`
	AvailableAt    string          `json:"available_at"`
	FavoriteID     json.RawMessage `json:"favorite_id"`
	FolderID       json.RawMessage `json:"folder_id"`
}

func (b *FavoriteAuthorityBinder) BindFactWithEvidence(ctx context.Context, source FactSourceEvidence) (usermodel.Event, error) {
	if b == nil || source.Event.Producer != favoriteFactProducer || source.Offset < 1 ||
		!factDeliveryHash.MatchString(source.InputHash) || source.Receipt.Producer != source.Event.Producer ||
		source.Receipt.EventID != source.Event.EventID || source.Receipt.InputHash != source.InputHash ||
		source.Receipt.Offset != source.Offset || source.Receipt.ReceiptID == "" || source.Receipt.TechnicalStatus != "accepted" {
		return usermodel.Event{}, fmt.Errorf("DC favorite evidence: %w", ErrFactDeliveryContract)
	}
	matches := favoriteEventID.FindStringSubmatch(source.Event.EventID)
	if len(matches) != 3 || source.Event.AggregateID != matches[1] ||
		(source.Event.EventType != "rtw.favorite.assert" && source.Event.EventType != "rtw.favorite.retract") {
		return usermodel.Event{}, fmt.Errorf("favorite event key and type: %w", ErrFactDeliveryContract)
	}
	if source.Event.SchemaVersion != 1 && source.Event.SchemaVersion != 2 {
		return usermodel.Event{}, fmt.Errorf("favorite schema version: %w", ErrFactDeliveryContract)
	}
	endpoint := b.baseURL + fmt.Sprintf("/internal/v%d/favorite/facts/", source.Event.SchemaVersion) +
		url.PathEscape(source.Event.Producer) + "/" + url.PathEscape(source.Event.EventID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return usermodel.Event{}, err
	}
	request.Header.Set("Authorization", "Bearer "+b.token)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	response, err := b.client.Do(request)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("read RTW favorite authority: %w: %w", ErrFactAuthorityUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return usermodel.Event{}, fmt.Errorf("RTW favorite authority HTTP %d: %w", response.StatusCode, ErrFactAuthorityUnavailable)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(body) > 256<<10 {
		return usermodel.Event{}, fmt.Errorf("RTW favorite authority response size: %w", ErrFactDeliveryContract)
	}
	var authority favoriteAuthorityResponse
	if err := decodeFavoriteAuthorityJSON(bytes.NewReader(body), &authority); err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW favorite authority response: %w", ErrFactDeliveryContract)
	}
	return bindFavoriteAuthority(source, authority)
}

func bindFavoriteAuthority(source FactSourceEvidence, authority favoriteAuthorityResponse) (usermodel.Event, error) {
	sourceCanonical, err := jsoncanonicalizer.Transform(authority.Event)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW source EventSpec JCS: %w", ErrFactDeliveryContract)
	}
	dcRaw, err := json.Marshal(source.Event)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("DC EventSpec: %w", ErrFactDeliveryContract)
	}
	dcCanonical, err := jsoncanonicalizer.Transform(dcRaw)
	if err != nil || !bytes.Equal(sourceCanonical, dcCanonical) {
		return usermodel.Event{}, fmt.Errorf("RTW source and DC EventSpec differ: %w", ErrFactDeliveryContract)
	}
	sum := sha256.Sum256(sourceCanonical)
	hash := hex.EncodeToString(sum[:])
	if hash != authority.SourceEventHash || hash != source.InputHash ||
		authority.TechnicalReceipt.InputHash != hash ||
		authority.TechnicalReceipt.EventID != source.Event.EventID ||
		authority.TechnicalReceipt.Producer != source.Event.Producer ||
		authority.TechnicalReceipt.TechnicalStatus != "accepted" ||
		authority.TechnicalReceipt.ReceiptID != source.Receipt.ReceiptID ||
		authority.TechnicalReceipt.Offset != source.Offset {
		return usermodel.Event{}, fmt.Errorf("RTW/DC immutable hash or receipt differs: %w", ErrFactDeliveryContract)
	}
	rtwReceived, err := time.Parse(time.RFC3339Nano, authority.TechnicalReceipt.ReceivedAt)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW receipt time: %w", ErrFactDeliveryContract)
	}
	dcReceived, err := time.Parse(time.RFC3339Nano, source.Receipt.ReceivedAt)
	if err != nil || !rtwReceived.Equal(dcReceived) {
		return usermodel.Event{}, fmt.Errorf("RTW/DC receipt time differs: %w", ErrFactDeliveryContract)
	}
	var frozen eventing.Event
	if err := decodeFavoriteAuthorityJSON(bytes.NewReader(authority.Event), &frozen); err != nil ||
		frozen.EventID != source.Event.EventID || frozen.SchemaVersion != source.Event.SchemaVersion {
		return usermodel.Event{}, fmt.Errorf("RTW frozen event shape: %w", ErrFactDeliveryContract)
	}
	var payload favoriteAuthorityPayload
	if err := decodeFavoriteAuthorityJSON(bytes.NewReader(frozen.Payload), &payload); err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW favorite payload shape: %w", ErrFactDeliveryContract)
	}
	favoriteID, ok := favoritePositiveID(payload.FavoriteID)
	if !ok || favoriteID != source.Event.AggregateID {
		return usermodel.Event{}, fmt.Errorf("RTW favorite identifier: %w", ErrFactDeliveryContract)
	}
	subject, err := parseFavoriteAuthoritySubject(source.Event.SchemaVersion, authority.SubjectRef)
	if err != nil {
		return usermodel.Event{}, err
	}
	fromPayload, err := parseFavoriteAuthoritySubject(source.Event.SchemaVersion, payload.SubjectRef)
	if err != nil {
		return usermodel.Event{}, err
	}
	authoritySubjectCanonical, err := jsoncanonicalizer.Transform(authority.SubjectRef)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW favorite subject JCS: %w", ErrFactDeliveryContract)
	}
	payloadSubjectCanonical, err := jsoncanonicalizer.Transform(payload.SubjectRef)
	if err != nil {
		return usermodel.Event{}, fmt.Errorf("RTW favorite payload subject JCS: %w", ErrFactDeliveryContract)
	}
	if _, ok := favoritePositiveID(payload.FolderID); !ok || subject != fromPayload ||
		!bytes.Equal(authoritySubjectCanonical, payloadSubjectCanonical) ||
		payload.SchemaVersion != source.Event.SchemaVersion ||
		payload.EventID != source.Event.EventID || payload.EventTime != source.Event.OccurredAt ||
		payload.AvailableAt != source.Event.OccurredAt || payload.TargetType == "" || payload.TargetID == "" ||
		payload.SourceRef != "rtw.favorite/"+favoriteID {
		return usermodel.Event{}, fmt.Errorf("RTW favorite source fields: %w", ErrFactDeliveryContract)
	}
	version := "1"
	action := usermodel.Assert
	if source.Event.EventType == "rtw.favorite.retract" {
		version, action = "2", usermodel.Retract
	}
	if source.Event.EventID != "favorite."+favoriteID+".v"+version ||
		source.Event.AggregateVersion != int64(version[0]-'0') || payload.Operation != string(action) {
		return usermodel.Event{}, fmt.Errorf("RTW favorite action and version: %w", ErrFactDeliveryContract)
	}
	fact := usermodel.Event{Subject: subject, Action: action, Kind: usermodel.ProductAction,
		Predicate: "favorite", ValueRef: payload.TargetType + "/" + payload.TargetID, ItemID: payload.TargetID}
	if payload.TargetRevision != nil {
		fact.ValueRef += "/revision/" + *payload.TargetRevision
	}
	if action == usermodel.Retract {
		expected := "favorite." + favoriteID + ".v1"
		if authority.PredecessorEventID != expected {
			return usermodel.Event{}, fmt.Errorf("RTW favorite predecessor: %w", ErrFactDeliveryContract)
		}
		fact.Supersedes = &usermodel.EventKey{Producer: favoriteFactProducer, EventID: authority.PredecessorEventID}
	} else if authority.PredecessorEventID != "" {
		return usermodel.Event{}, fmt.Errorf("RTW assert has predecessor: %w", ErrFactDeliveryContract)
	}
	return fact, nil
}

type favoriteSubjectV2 struct {
	Issuer    string `json:"issuer"`
	SubjectID string `json:"subject_id"`
}

func parseFavoriteAuthoritySubject(schema int, raw json.RawMessage) (usermodel.SubjectRef, error) {
	var subject usermodel.SubjectRef
	switch schema {
	case 1:
		if err := decodeFavoriteAuthorityJSON(bytes.NewReader(raw), &subject); err != nil ||
			subject.AuthorityID != "rtw.identity" || subject.TenantID != "platform" {
			return usermodel.SubjectRef{}, fmt.Errorf("RTW favorite v1 subject: %w", ErrFactDeliveryContract)
		}
	case 2:
		var v2 favoriteSubjectV2
		if err := decodeFavoriteAuthorityJSON(bytes.NewReader(raw), &v2); err != nil || v2.Issuer != "rtw.identity" {
			return usermodel.SubjectRef{}, fmt.Errorf("RTW favorite v2 subject: %w", ErrFactDeliveryContract)
		}
		subject = usermodel.SubjectRef{AuthorityID: v2.Issuer, TenantID: "platform", SubjectID: v2.SubjectID}
	default:
		return usermodel.SubjectRef{}, fmt.Errorf("RTW favorite subject version: %w", ErrFactDeliveryContract)
	}
	uid, err := strconv.ParseInt(subject.SubjectID, 10, 64)
	if err != nil || uid < 1 || strconv.FormatInt(uid, 10) != subject.SubjectID {
		return usermodel.SubjectRef{}, fmt.Errorf("RTW favorite UID: %w", ErrFactDeliveryContract)
	}
	return subject, nil
}

func favoritePositiveID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	value := string(raw)
	quoted := raw[0] == '"'
	if quoted && json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return value, err == nil && id > 0 && strconv.FormatInt(id, 10) == value && (quoted || id <= maxLegacyExactJSONID)
}

func decodeFavoriteAuthorityJSON(reader io.Reader, out any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing favorite authority JSON")
	}
	return nil
}
