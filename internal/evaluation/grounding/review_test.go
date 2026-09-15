package grounding

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func caseBytes(t *testing.T, value Case) []byte {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func signedFixture(t *testing.T, value Case, caseRaw []byte, claim Claim,
	coverageComplete bool, kinds ...string) ([]byte, *Trust) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kind := "synthetic_fixture"
	if len(kinds) == 1 {
		kind = kinds[0]
	}
	payload := ReviewPayload{SchemaVersion: ReviewSchema, CaseSHA256: Digest(caseRaw),
		PolicyRevision: ReviewPolicy, DataKind: kind,
		ReviewerAuthority: RTWReviewerAuthority, ReviewerID: "fixture-reviewer",
		ReviewedAt: "2026-09-15T00:00:00Z", CoverageComplete: coverageComplete,
		Claims: []Claim{claim}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	review := Review{Payload: payload, Signature: hex.EncodeToString(ed25519.Sign(private, encoded))}
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	trust := &Trust{SchemaVersion: TrustSchema, DataKind: kind,
		Authority: payload.ReviewerAuthority, ReviewerID: payload.ReviewerID,
		PublicKey: hex.EncodeToString(public)}
	return raw, trust
}

func fixtureCase(t *testing.T, quote, answer string) Case {
	t.Helper()
	value := Case{SchemaVersion: CaseSchema, PolicyRevision: ReviewPolicy,
		ReviewStatus: "pending_human_review", Activation: "none", SearchID: "search-fixture",
		AnswerID: "answer-fixture", ModelInteractionID: "dc-interaction-fixture",
		ModelResponseSHA256:   Digest([]byte("model response")),
		RTWAnswerReportSHA256: Digest([]byte("RTW answer")),
		DCUsageReportSHA256:   Digest([]byte("DC usage")),
		RTWTraceLogSHA256:     Digest([]byte("RTW structured trace")),
		RTWTraceID:            "0123456789abcdef0123456789abcdef",
		TraceScope:            "rtw_structured_log_not_otlp_collector",
		CitationPackRef:       citationRef("search-fixture"),
		Evidence:              []Evidence{{ID: "ev-fixture", Quote: quote, QuoteSHA256: Digest([]byte(quote))}},
		AcceptedAnswer:        answer, AcceptedAnswerSHA256: Digest([]byte(answer)),
		PriorQualityObservation: "not_assessed"}
	identity, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	value.CaseID = "grounding-" + Digest(identity)
	return value
}

func TestCaseRequiresRTWDurableRefFromSearchID(t *testing.T) {
	value := fixtureCase(t, "A source quote.", "A source quote.")
	value.CitationPackRef = citationRef("another-search")
	value.CaseID = ""
	identity, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	value.CaseID = "grounding-" + Digest(identity)
	if ValidateCase(value) == nil {
		t.Fatal("case accepted a durable_ref not derived from its search_id")
	}
}

func TestSyntheticSignedUnsupportedClaimRejectsWithoutTextHeuristics(t *testing.T) {
	value := fixtureCase(t, "The room is quiet.", "The room is quiet, and the lights are blue.")
	caseRaw := caseBytes(t, value)
	start := strings.Index(value.AcceptedAnswer, "the lights are blue")
	claim := Claim{StartByte: start, EndByte: start + len("the lights are blue"),
		Text: "the lights are blue", EvidenceIDs: []string{"ev-fixture"},
		Label: "unsupported", Reason: "The quoted source says nothing about light color."}
	reviewRaw, trust := signedFixture(t, value, caseRaw, claim, false)
	result, err := Evaluate(value, caseRaw, nil, nil)
	if err != nil || result.Decision != "pending_human_review" || result.RuntimeBlocking {
		t.Fatalf("unreviewed case was automatically decided: %+v %v", result, err)
	}
	result, err = Evaluate(value, caseRaw, reviewRaw, trust)
	if err != nil || result.Decision != "synthetic_rejected_unsupported" || result.Unsupported != 1 ||
		result.EvidenceLevel != "synthetic_fixture" || result.RuntimeBlocking {
		t.Fatalf("signed synthetic unsupported span did not reject offline: %+v %v", result, err)
	}
	selfDeclaredHuman, selfTrust := signedFixture(t, value, caseRaw, claim, false, "human_admin")
	result, err = Evaluate(value, caseRaw, selfDeclaredHuman, selfTrust)
	if err != nil || result.Decision != "pending_authority_registry" ||
		result.EvidenceLevel != "self_supplied_key_not_RTW_registry" || result.RuntimeBlocking {
		t.Fatalf("self-supplied human_admin key was promoted: %+v %v", result, err)
	}
	bad := append([]byte(nil), reviewRaw...)
	bad[len(bad)-3] ^= 1
	if _, err := Evaluate(value, caseRaw, bad, trust); err == nil {
		t.Fatal("tampered review signature was accepted")
	}
	wrong := value
	wrong.Evidence = []Evidence{{ID: "ev-fixture", Quote: "different", QuoteSHA256: Digest([]byte("different"))}}
	if _, err := Evaluate(wrong, caseRaw, reviewRaw, trust); err == nil {
		t.Fatal("changed quote crossed the frozen case binding")
	}
}

func TestSyntheticSupportedDecisionRequiresWholeAnswerCoverage(t *testing.T) {
	value := fixtureCase(t, "The room is quiet.", "The room is quiet.")
	caseRaw := caseBytes(t, value)
	short := Claim{StartByte: 0, EndByte: len("The room"), Text: "The room",
		EvidenceIDs: []string{"ev-fixture"}, Label: "supported", Reason: "Literal source text."}
	reviewRaw, trust := signedFixture(t, value, caseRaw, short, true)
	result, err := Evaluate(value, caseRaw, reviewRaw, trust)
	if err != nil || result.Decision != "synthetic_pending_human_review" {
		t.Fatalf("partial coverage became supported: %+v %v", result, err)
	}
	full := short
	full.EndByte, full.Text = len(value.AcceptedAnswer), value.AcceptedAnswer
	reviewRaw, trust = signedFixture(t, value, caseRaw, full, true)
	result, err = Evaluate(value, caseRaw, reviewRaw, trust)
	if err != nil || result.Decision != "synthetic_supported_review_complete" || result.Supported != 1 {
		t.Fatalf("signed complete source-supported fixture not distinguished: %+v %v", result, err)
	}
}

func TestRealRTWDCTraceCaseRemainsPendingWithoutHumanReceipt(t *testing.T) {
	usagePath, rtwPath, tracePath := os.Getenv("SEA_GROUNDING_USAGE_REPORT"),
		os.Getenv("SEA_GROUNDING_RTW_REPORT"), os.Getenv("SEA_GROUNDING_RTW_TRACE_LOG")
	if usagePath == "" || rtwPath == "" || tracePath == "" {
		t.Skip("set same-run RTW/DC/structured-trace private evidence paths")
	}
	value, err := FreezeCase(usagePath, rtwPath, tracePath)
	if err != nil || ValidateCase(value) != nil || value.TraceScope != "rtw_structured_log_not_otlp_collector" {
		t.Fatalf("same-run RTW/DC case source failed: %+v %v", value, err)
	}
	caseRaw := caseBytes(t, value)
	result, err := Evaluate(value, caseRaw, nil, nil)
	if err != nil || result.Decision != "pending_human_review" || result.RuntimeBlocking {
		t.Fatalf("real accepted answer was auto-labeled without human review: %+v %v", result, err)
	}
	traceBytes, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	var withoutCitation [][]byte
	for _, line := range bytes.Split(traceBytes, []byte{'\n'}) {
		if bytes.Contains(line, []byte(`"event":"knowledge.search.citations.accept.succeeded"`)) &&
			bytes.Contains(line, []byte(value.SearchID)) {
			continue
		}
		withoutCitation = append(withoutCitation, line)
	}
	badTracePath := filepath.Join(t.TempDir(), "missing-citation.jsonl")
	if err := os.WriteFile(badTracePath, bytes.Join(withoutCitation, []byte{'\n'}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FreezeCase(usagePath, rtwPath, badTracePath); err == nil {
		t.Fatal("RTW answer trace without same-trace citation acceptance was frozen")
	}
	rtwBytes, err := os.ReadFile(rtwPath)
	if err != nil {
		t.Fatal(err)
	}
	var changed map[string]any
	if err := json.Unmarshal(rtwBytes, &changed); err != nil {
		t.Fatal(err)
	}
	changed["quote"] = "different published quote"
	changedRaw, _ := json.Marshal(changed)
	changedPath := filepath.Join(t.TempDir(), "changed-rtw.json")
	if err := os.WriteFile(changedPath, changedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FreezeCase(usagePath, changedPath, tracePath); err == nil {
		t.Fatal("changed RTW quote crossed model/answer/source hash binding")
	}
	// This exercise is explicitly synthetic. It demonstrates that an actual
	// answer's unsupported span can be rejected only when an external label is
	// supplied; it is not a claim that a human reviewed this real answer.
	text := "ranked highly among search results"
	start := strings.Index(value.AcceptedAnswer, text)
	if start < 0 {
		t.Skip("this opt-in source has a different answer; generic tests still cover the gate")
	}
	claim := Claim{StartByte: start, EndByte: start + len(text), Text: text,
		EvidenceIDs: []string{value.Evidence[0].ID}, Label: "unsupported",
		Reason: "Synthetic fixture label: cited quote lacks any ranking observation."}
	reviewRaw, trust := signedFixture(t, value, caseRaw, claim, false)
	result, err = Evaluate(value, caseRaw, reviewRaw, trust)
	if err != nil || result.Decision != "synthetic_rejected_unsupported" || result.Activation != "none" {
		t.Fatalf("actual quote/answer binding did not support a synthetic negative test: %+v %v", result, err)
	}
	if output := os.Getenv("SEA_GROUNDING_SYNTHETIC_PROOF_DIR"); output != "" {
		if err := os.Mkdir(output, 0700); err != nil {
			t.Fatal(err)
		}
		trustRaw, err := json.MarshalIndent(trust, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		decisionRaw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		for name, raw := range map[string][]byte{
			"synthetic-review.json":   reviewRaw,
			"synthetic-trust.json":    trustRaw,
			"synthetic-decision.json": decisionRaw,
		} {
			file, err := os.OpenFile(filepath.Join(output, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write(raw); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
