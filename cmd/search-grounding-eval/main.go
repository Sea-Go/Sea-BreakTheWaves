// Command search-grounding-eval freezes RTW/DC/trace evidence and evaluates
// signed human claim labels offline. It never changes live answer serving.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sea-Go/Sea-BreakTheWaves/internal/evaluation/grounding"
)

func writeNew(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readBounded(path string, limit int64, private bool) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit ||
		private && info.Mode().Perm()&0077 != 0 {
		return nil, grounding.ErrEvidence
	}
	return os.ReadFile(path)
}

func freeze(usagePath, rtwPath, tracePath, outputDir string) error {
	value, err := grounding.FreezeCase(usagePath, rtwPath, tracePath)
	if err != nil {
		return err
	}
	if err := os.Mkdir(outputDir, 0700); err != nil {
		return err
	}
	if err := writeNew(filepath.Join(outputDir, "case.json"), value); err != nil {
		return err
	}
	caseRaw, err := readBounded(filepath.Join(outputDir, "case.json"), 1<<20, true)
	if err != nil {
		return err
	}
	template := struct {
		SchemaVersion string               `json:"schema_version"`
		Status        string               `json:"status"`
		CaseID        string               `json:"case_id"`
		CaseSHA256    string               `json:"case_sha256"`
		Policy        string               `json:"policy_revision"`
		Answer        string               `json:"accepted_answer"`
		Evidence      []grounding.Evidence `json:"evidence"`
		Instructions  []string             `json:"instructions"`
	}{"sea.search.answer-grounding-review-template.v1", "pending_human_review",
		value.CaseID, grounding.Digest(caseRaw), grounding.ReviewPolicy, value.AcceptedAnswer,
		value.Evidence, []string{
			"RTW admin reviewer identifies exact UTF-8 byte spans of the answer and the cited evidence IDs.",
			"For every claim set supported, unsupported, or uncertain and explain the source comparison.",
			"Sign ReviewPayload with the RTW-approved Ed25519 reviewer key; no model-generated label counts as human evidence.",
			"A supported decision requires complete non-whitespace answer coverage; any signed unsupported span rejects offline.",
		}}
	return writeNew(filepath.Join(outputDir, "review-template.json"), template)
}

func readCase(casePath string) (grounding.Case, []byte, error) {
	var value grounding.Case
	caseRaw, err := readBounded(casePath, 1<<20, true)
	if err != nil {
		return value, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(caseRaw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return grounding.Case{}, nil, grounding.ErrEvidence
	}
	return value, caseRaw, nil
}

func evaluate(casePath, reviewPath, trustPath, outputPath string) error {
	value, caseRaw, err := readCase(casePath)
	if err != nil {
		return err
	}
	var reviewRaw []byte
	var trust *grounding.Trust
	if reviewPath != "" || trustPath != "" {
		if reviewPath == "" || trustPath == "" {
			return errors.New("signed review and trust root must be supplied together")
		}
		reviewRaw, err = readBounded(reviewPath, 1<<20, true)
		if err != nil {
			return err
		}
		trustRaw, err := readBounded(trustPath, 1<<20, false)
		if err != nil {
			return err
		}
		var parsed grounding.Trust
		if json.Unmarshal(trustRaw, &parsed) != nil {
			return grounding.ErrEvidence
		}
		trust = &parsed
	}
	result, err := grounding.Evaluate(value, caseRaw, reviewRaw, trust)
	if err != nil {
		return err
	}
	return writeNew(outputPath, result)
}

func evaluateRTW(casePath, usagePath, rtwPath, tracePath, baseURL, tokenFile, outputPath string) error {
	value, caseRaw, err := readCase(casePath)
	if err != nil {
		return err
	}
	// RTW PG cannot self-attest this run's model response or structured trace.
	// Refreeze from the original private sources before consulting the Worker
	// review/key registry, so a caller cannot substitute invented case fields.
	refrozen, err := grounding.FreezeCase(usagePath, rtwPath, tracePath)
	if err != nil {
		return err
	}
	refrozenRaw, err := json.MarshalIndent(refrozen, "", "  ")
	if err != nil {
		return err
	}
	refrozenRaw = append(refrozenRaw, '\n')
	if grounding.Digest(caseRaw) != grounding.Digest(refrozenRaw) ||
		!bytes.Equal(caseRaw, refrozenRaw) {
		return grounding.ErrEvidence
	}
	rawToken, err := readBounded(tokenFile, 4096, true)
	if err != nil {
		return err
	}
	worker, err := grounding.NewHTTPAuthority(baseURL, strings.TrimSpace(string(rawToken)))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := grounding.EvaluateFromRTW(ctx, value, caseRaw, worker)
	if err != nil {
		return err
	}
	return writeNew(outputPath, result)
}

func main() {
	var mode, usagePath, rtwPath, tracePath, casePath, reviewPath, trustPath, outputPath, outputDir string
	var workerURL, tokenFile string
	flag.StringVar(&mode, "mode", "", "freeze, evaluate, or evaluate-rtw")
	flag.StringVar(&usagePath, "usage-report", "", "private DC/RTW exact usage receipt")
	flag.StringVar(&rtwPath, "rtw-report", "", "private RTW accepted answer receipt")
	flag.StringVar(&tracePath, "rtw-trace-log", "", "private RTW structured JSONL trace evidence")
	flag.StringVar(&outputDir, "output-dir", "", "new private directory for case and review template")
	flag.StringVar(&casePath, "case", "", "frozen grounding case")
	flag.StringVar(&reviewPath, "review", "", "optional signed human review")
	flag.StringVar(&trustPath, "trust", "", "self-supplied local fixture key; never proves human_admin authority")
	flag.StringVar(&workerURL, "rtw-worker-url", "", "configured RTW Worker base URL; caller input does not prove service identity")
	flag.StringVar(&tokenFile, "worker-token-file", "", "private configured RTW Worker bearer file")
	flag.StringVar(&outputPath, "output", "", "new offline decision file")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	var err error
	switch mode {
	case "freeze":
		if usagePath == "" || rtwPath == "" || tracePath == "" || outputDir == "" {
			err = errors.New("freeze requires all source reports and a new output directory")
		} else {
			err = freeze(usagePath, rtwPath, tracePath, outputDir)
		}
	case "evaluate":
		if casePath == "" || outputPath == "" {
			err = errors.New("evaluate requires frozen case and new decision path")
		} else {
			err = evaluate(casePath, reviewPath, trustPath, outputPath)
		}
	case "evaluate-rtw":
		if casePath == "" || usagePath == "" || rtwPath == "" || tracePath == "" ||
			outputPath == "" || workerURL == "" || tokenFile == "" ||
			reviewPath != "" || trustPath != "" {
			err = errors.New("evaluate-rtw needs case, original usage/RTW/trace sources, Worker URL/token file and output; local review/trust are forbidden")
		} else {
			err = evaluateRTW(casePath, usagePath, rtwPath, tracePath, workerURL, tokenFile, outputPath)
		}
	default:
		err = errors.New("mode must be freeze, evaluate or evaluate-rtw")
	}
	if err != nil {
		logger.Error("answer grounding acceptance failed", "mode", mode, "error", err)
		os.Exit(1)
	}
	logger.Info("answer grounding offline acceptance completed", "mode", mode)
}
