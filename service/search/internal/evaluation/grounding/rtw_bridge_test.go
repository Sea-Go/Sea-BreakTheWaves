package grounding

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// The RTW parent owns a real isolated Knowledge HTTP/PostgreSQL service and
// passes only a disposable synthetic case/key bridge. This is deliberately a
// direct authority read; the production CLI additionally refreezes DC/trace.
func TestRealRTWGroundingWorkerBridge(t *testing.T) {
	bridgePath := os.Getenv("SEA_RTW_GROUNDING_BRIDGE_FILE")
	if bridgePath == "" {
		t.Skip("requires RTW's isolated synthetic reviewer Worker bridge")
	}
	bridgeRaw, err := readPrivate(bridgePath, 16<<10)
	if err != nil {
		t.Fatal(err)
	}
	var bridge struct {
		RTWBase             string `json:"rtw_base"`
		WorkerToken         string `json:"worker_token"`
		CaseSHA256          string `json:"case_sha256"`
		CasePath            string `json:"case_path"`
		ReviewReceiptSHA256 string `json:"review_receipt_sha256"`
		KeyID               string `json:"key_id"`
		UsagePath           string `json:"usage_path"`
		RTWReportPath       string `json:"rtw_report_path"`
		TracePath           string `json:"trace_path"`
		ReleaseFile         string `json:"release_file"`
		RevokedReadyFile    string `json:"revoked_ready_file"`
		RevokedReleaseFile  string `json:"revoked_release_file"`
	}
	if json.Unmarshal(bridgeRaw, &bridge) != nil || bridge.RTWBase == "" || bridge.WorkerToken == "" ||
		bridge.CasePath == "" || !hashPattern.MatchString(bridge.CaseSHA256) ||
		!hashPattern.MatchString(bridge.ReviewReceiptSHA256) || bridge.KeyID == "" || bridge.ReleaseFile == "" ||
		bridge.RevokedReadyFile == "" || bridge.RevokedReleaseFile == "" ||
		bridge.UsagePath == "" || bridge.RTWReportPath == "" || bridge.TracePath == "" {
		t.Fatal("RTW synthetic bridge contract differs")
	}
	defer func() { _ = os.WriteFile(bridge.ReleaseFile, []byte("release\n"), 0600) }()
	defer func() { _ = os.WriteFile(bridge.RevokedReleaseFile, []byte("release\n"), 0600) }()
	caseRaw, err := readPrivate(bridge.CasePath, 1<<20)
	if err != nil || Digest(caseRaw) != bridge.CaseSHA256 {
		t.Fatal("RTW bridge case original bytes changed")
	}
	var value Case
	decoder := json.NewDecoder(strings.NewReader(string(caseRaw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || ValidateCase(value) != nil {
		t.Fatal("RTW bridge case failed BTW source contract")
	}
	refrozen, err := FreezeCase(bridge.UsagePath, bridge.RTWReportPath, bridge.TracePath)
	if err != nil {
		t.Fatalf("RTW synthetic bridge source refreeze failed: %v", err)
	}
	refrozenRaw, err := json.MarshalIndent(refrozen, "", "  ")
	if err != nil || string(append(refrozenRaw, '\n')) != string(caseRaw) {
		t.Fatal("RTW synthetic bridge case differs from source reports/trace")
	}
	client, err := NewHTTPAuthority(bridge.RTWBase, bridge.WorkerToken)
	if err != nil {
		t.Fatal(err)
	}
	evaluate := func(target *HTTPAuthority) (Result, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return EvaluateFromRTW(ctx, value, caseRaw, target)
	}
	result, err := evaluate(client)
	if err != nil || result.Decision != "synthetic_rejected_unsupported" ||
		result.ReviewSHA256 != bridge.ReviewReceiptSHA256 ||
		result.EvidenceLevel != "rtw_registry_synthetic_fixture" || result.RuntimeBlocking {
		t.Fatalf("real RTW PG/Worker signed synthetic review did not bind: %+v %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	receipt, receiptErr := client.Review(ctx, bridge.CaseSHA256)
	citation, citationErr := client.Citation(ctx, value.SearchID)
	activeKey, keyErr := client.ReviewerKey(ctx, bridge.KeyID)
	cancel()
	if receiptErr != nil || citationErr != nil || keyErr != nil ||
		receipt.CitationPackRef != citation.DurableRef || receipt.CitationPackSHA256 != citation.PackHash ||
		receipt.CitationPackRef != citationRef(value.SearchID) ||
		receipt.CitationPackSHA256 == strings.TrimPrefix(receipt.CitationPackRef, citationRefPrefix) ||
		receipt.TraceAuthorityStatus != "external_case_unverified" || activeKey.Status != "active" {
		t.Fatalf("real RTW authority identities differ: receipt=%+v citation=%+v key=%+v errors=%v/%v/%v",
			receipt, citation, activeKey, receiptErr, citationErr, keyErr)
	}
	wrong, _ := NewHTTPAuthority(bridge.RTWBase, "wrong-worker-token")
	if _, err := evaluate(wrong); err == nil {
		t.Fatal("unauthorized Worker credential fell back to local trust")
	}
	if err := os.WriteFile(bridge.ReleaseFile, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(bridge.RevokedReadyFile); err == nil {
			break
		}
	}
	if _, err := os.Stat(bridge.RevokedReadyFile); err != nil {
		t.Fatal("RTW reviewer key revocation was not ready")
	}
	revoked, err := evaluate(client)
	if err != nil || revoked.Decision != "pending_revoked_authority" ||
		revoked.EvidenceLevel != "rtw_registry_revoked" || revoked.RuntimeBlocking {
		t.Fatalf("real RTW revoked key retained current authority: %+v %v", revoked, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	revokedKey, keyErr := client.ReviewerKey(ctx, bridge.KeyID)
	cancel()
	if keyErr != nil || revokedKey.Status != "revoked" ||
		revokedKey.RegistryRevision <= activeKey.RegistryRevision || revokedKey.RevokedAt == "" {
		t.Fatalf("real RTW current revoked registry differs: %+v %v", revokedKey, keyErr)
	}
	if path := os.Getenv("SEA_GROUNDING_BRIDGE_PROOF_OUTPUT"); path != "" {
		proof := struct {
			SchemaVersion           string `json:"schema_version"`
			CaseSHA256              string `json:"case_sha256"`
			ReviewSHA256            string `json:"review_sha256"`
			KeyID                   string `json:"key_id"`
			DurableRef              string `json:"durable_ref"`
			CitationPackSHA256      string `json:"citation_pack_sha256"`
			PackIdentityDistinct    bool   `json:"pack_identity_distinct"`
			TraceAuthorityStatus    string `json:"trace_authority_status"`
			ActiveRegistryRevision  int64  `json:"active_registry_revision"`
			RevokedRegistryRevision int64  `json:"revoked_registry_revision"`
			ActiveDecision          string `json:"active_decision"`
			RevokedDecision         string `json:"revoked_decision"`
			EvidenceLevel           string `json:"evidence_level"`
			Activation              string `json:"activation"`
			RuntimeBlocking         bool   `json:"runtime_blocking"`
		}{"sea.search.rtw-grounding-bridge.v1", bridge.CaseSHA256, result.ReviewSHA256,
			bridge.KeyID, citation.DurableRef, citation.PackHash,
			citation.PackHash != strings.TrimPrefix(citation.DurableRef, citationRefPrefix),
			receipt.TraceAuthorityStatus, activeKey.RegistryRevision, revokedKey.RegistryRevision,
			result.Decision, revoked.Decision, result.EvidenceLevel, result.Activation,
			result.RuntimeBlocking || revoked.RuntimeBlocking}
		raw, err := json.MarshalIndent(proof, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(raw, '\n')); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(bridge.RevokedReleaseFile, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
