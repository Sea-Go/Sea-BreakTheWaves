package preflight

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const SourceCommit = "f51723e0a1db3b5029342ef8207fb2255be79cec"

// The contract is produced from an isolated PG16 instance after applying only
// migrations/usermodel/001..006 in deployment order. Audit never applies DDL.
//
//go:embed contract.json
var contractBytes []byte

type frozenContract struct {
	SourceSHA256 string  `json:"source_sha256"`
	Catalog      Catalog `json:"catalog"`
}

type Finding struct {
	Level string `json:"level"`
	Rule  string `json:"rule"`
	Table string `json:"table,omitempty"`
	Count int64  `json:"count"`
}

type TableScan struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

type Report struct {
	SourceCommit      string      `json:"source_commit"`
	SourceSHA256      string      `json:"source_sha256"`
	ContractSHA256    string      `json:"contract_sha256"`
	LiveCatalogSHA256 string      `json:"live_catalog_sha256"`
	Transaction       string      `json:"transaction"`
	Scope             string      `json:"scope"`
	TableRows         []TableScan `json:"table_rows"`
	TotalRows         int64       `json:"total_rows"`
	RowAuditComplete  bool        `json:"row_audit_complete"`
	Findings          []Finding   `json:"findings"`
	L1                int64       `json:"l1"`
	L2                int64       `json:"l2"`
	L3                int64       `json:"l3"`
	// Hash of compact json.Marshal with this field empty. CLI stdout file bytes
	// include the filled field and a newline, and have a different SHA-256.
	ReportSHA256 string `json:"report_sha256"`
}

// Observation deliberately contains only bounded rule/table/count labels.
// A CLI may route it to its controlled structured stderr logger.
type Observation struct {
	Stage   string
	Outcome string
	Table   string
	Count   int64
}

type Observer func(Observation)

func observe(fn Observer, e Observation) {
	if fn != nil {
		fn(e)
	}
}

func Contract() (Catalog, string, error) {
	var frozen frozenContract
	if err := json.Unmarshal(contractBytes, &frozen); err != nil {
		return Catalog{}, "", err
	}
	if err := frozen.Catalog.ValidateNames(); err != nil {
		return Catalog{}, "", err
	}
	return frozen.Catalog, frozen.SourceSHA256, nil
}

// Run reads exactly one repeatable-read, read-only snapshot. It does not
// return row identifiers, JSON bodies, SQL text, or connection information.
func Run(ctx context.Context, conn *pgx.Conn, schema string, observer Observer) (Report, error) {
	var report Report
	if schema == "" {
		return report, errors.New("schema is required")
	}
	want, sourceSHA, err := Contract()
	if err != nil {
		return report, errors.New("frozen schema contract is invalid")
	}
	report.SourceCommit = SourceCommit
	report.SourceSHA256 = sourceSHA
	report.ContractSHA256 = want.SHA256()
	report.Transaction = "repeatable_read/read_only"
	report.Scope = "usermodel migrations 001..006; production not verified by this report"
	observe(observer, Observation{Stage: "usermodel.preflight", Outcome: "started"})
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return report, errors.New("read-only transaction could not start")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL search_path TO `+pgx.Identifier{schema}.Sanitize()); err != nil {
		return report, errors.New("schema search path could not be fixed")
	}
	var readOnly, isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil {
		return report, errors.New("read-only transaction could not be verified")
	}
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return report, errors.New("transaction isolation could not be verified")
	}
	if readOnly != "on" || isolation != "repeatable read" {
		return report, errors.New("transaction is not repeatable-read and read-only")
	}
	live, err := readCatalog(ctx, tx, schema)
	if err != nil {
		return report, errors.New("physical catalog could not be read")
	}
	report.LiveCatalogSHA256 = live.SHA256()
	missingColumns := compareCatalog(&report, want, live)
	observe(observer, Observation{Stage: "usermodel.preflight.catalog", Outcome: "completed", Count: report.L1})
	if missingColumns {
		finalize(&report)
		observe(observer, Observation{Stage: "usermodel.preflight", Outcome: "blocked", Count: report.L1})
		return report, nil
	}
	if err := scanRows(ctx, tx, schema, want, &report); err != nil {
		return Report{}, errors.New("table row scan did not complete")
	}
	if err := auditRows(ctx, tx, schema, want, &report); err != nil {
		return Report{}, errors.New("row audit did not complete")
	}
	report.RowAuditComplete = true
	finalize(&report)
	outcome := "passed"
	if report.L1+report.L2 > 0 {
		outcome = "blocked"
	} else if report.L3 > 0 {
		outcome = "review"
	}
	observe(observer, Observation{Stage: "usermodel.preflight", Outcome: outcome, Count: report.L1 + report.L2 + report.L3})
	return report, nil
}

func compareCatalog(report *Report, want, live Catalog) bool {
	actual := live.ByName()
	missingColumns := false
	for _, expected := range want.Tables {
		got, found := actual[expected.Name]
		if !found {
			add(report, "L1", "missing_table", expected.Name, 1)
			missingColumns = true
			continue
		}
		if !reflect.DeepEqual(expected.Columns, got.Columns) {
			add(report, "L1", "column_drift", expected.Name, 1)
			missingColumns = true
		}
		if !equalConstraints(expected.Constraints, got.Constraints) {
			missing := int64(0)
			for _, con := range expected.Constraints {
				if !containsConstraint(got.Constraints, con) {
					missing++
				}
			}
			add(report, "L1", "missing_or_changed_constraint", expected.Name, missing)
			if missing == 0 {
				add(report, "L3", "extra_constraint", expected.Name, 1)
			}
		}
		if !equalUnique(expected.Unique, got.Unique) {
			missing := int64(0)
			for _, key := range expected.Unique {
				if !containsUnique(got.Unique, key) {
					missing++
				}
			}
			add(report, "L1", "missing_or_changed_unique_key", expected.Name, missing)
			if missing == 0 {
				add(report, "L3", "extra_unique_key", expected.Name, 1)
			}
		}
	}
	return missingColumns
}

func containsConstraint(list []Constraint, target Constraint) bool {
	for _, got := range list {
		if got.Name == target.Name && got.Kind == target.Kind &&
			slices.Equal(got.Columns, target.Columns) && got.RefTable == target.RefTable &&
			slices.Equal(got.RefColumns, target.RefColumns) && got.Definition == target.Definition {
			return true
		}
	}
	return false
}

func containsUnique(list []UniqueKey, target UniqueKey) bool {
	for _, got := range list {
		if got.Name == target.Name && slices.Equal(got.Columns, target.Columns) && got.Predicate == target.Predicate {
			return true
		}
	}
	return false
}

func equalConstraints(a, b []Constraint) bool {
	if len(a) != len(b) {
		return false
	}
	for _, expected := range a {
		if !containsConstraint(b, expected) {
			return false
		}
	}
	return true
}

func equalUnique(a, b []UniqueKey) bool {
	if len(a) != len(b) {
		return false
	}
	for _, expected := range a {
		if !containsUnique(b, expected) {
			return false
		}
	}
	return true
}

func add(report *Report, level, rule, table string, count int64) {
	if count <= 0 {
		return
	}
	report.Findings = append(report.Findings, Finding{Level: level, Rule: rule, Table: table, Count: count})
	switch level {
	case "L1":
		report.L1 += count
	case "L2":
		report.L2 += count
	case "L3":
		report.L3 += count
	}
}

func finalize(report *Report) {
	sort.Slice(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Table < b.Table
	})
	copy := *report
	copy.ReportSHA256 = ""
	b, _ := json.Marshal(copy)
	h := sha256.Sum256(b)
	report.ReportSHA256 = hex.EncodeToString(h[:])
}

// Scans include every scoped table in the same read-only snapshot. Row counts
// contain no identifiers but can still disclose small populations.
func scanRows(ctx context.Context, tx pgx.Tx, schema string, contract Catalog, report *Report) error {
	for _, table := range contract.Tables {
		var rows int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+tableName(schema, table.Name)).Scan(&rows); err != nil {
			return err
		}
		if rows < 0 || report.TotalRows > math.MaxInt64-rows {
			return errors.New("table row count overflow")
		}
		report.TableRows = append(report.TableRows, TableScan{Table: table.Name, Rows: rows})
		report.TotalRows += rows
	}
	return nil
}

func auditRows(ctx context.Context, tx pgx.Tx, schema string, contract Catalog, report *Report) error {
	for _, t := range contract.Tables {
		cols := columnSet(t)
		if cols["authority_id"] && cols["tenant_id"] {
			identity := `authority_id <> 'rtw.identity' OR tenant_id <> 'platform'`
			if cols["subject_id"] {
				identity += ` OR NOT (` + positiveUID("subject_id") + `)`
			}
			if t.Name == "usermodel_unmapped_events" {
				identity += ` OR (bound_subject_id IS NOT NULL AND NOT (` + positiveUID("bound_subject_id") + `))`
			}
			if err := countWhere(ctx, tx, report, "L1", "invalid_legacy_subject", t.Name,
				`SELECT count(*) FROM `+tableName(schema, t.Name)+` WHERE `+identity); err != nil {
				return err
			}
		}
		for _, key := range t.Unique {
			if !sliceContains(key.Columns, "tenant_id") {
				continue
			}
			projected := make([]string, 0, len(key.Columns)-1)
			for _, col := range key.Columns {
				if col != "tenant_id" {
					projected = append(projected, pgx.Identifier{col}.Sanitize())
				}
			}
			if len(projected) == 0 {
				continue
			}
			where := ""
			if key.Predicate != "" {
				where = " WHERE " + key.Predicate
			}
			query := `SELECT count(*) FROM (SELECT 1 FROM ` + tableName(schema, t.Name) + where +
				` GROUP BY ` + strings.Join(projected, ",") + ` HAVING count(*)>1) collision`
			if err := countWhere(ctx, tx, report, "L1", "projected_unique_collision:"+key.Name, t.Name, query); err != nil {
				return err
			}
		}
		for _, fk := range t.Constraints {
			if fk.Kind != "f" {
				continue
			}
			parent := contract.ByName()[fk.RefTable]
			if parent.Name == "" {
				continue
			}
			conditions := make([]string, 0, len(fk.Columns))
			for i, col := range fk.Columns {
				conditions = append(conditions, "c."+pgx.Identifier{col}.Sanitize()+"=p."+pgx.Identifier{fk.RefColumns[i]}.Sanitize())
			}
			query := `SELECT count(*) FROM ` + tableName(schema, t.Name) + ` c WHERE NOT EXISTS (` +
				`SELECT 1 FROM ` + tableName(schema, parent.Name) + ` p WHERE ` + strings.Join(conditions, " AND ") + `)`
			if err := countWhere(ctx, tx, report, "L2", "orphan_expected_fk:"+fk.Name, t.Name, query); err != nil {
				return err
			}
		}
	}
	return auditVersions(ctx, tx, schema, report)
}

func positiveUID(col string) string {
	q := pgx.Identifier{col}.Sanitize()
	return q + ` ~ '^[1-9][0-9]*$' AND (length(` + q + `) < 19 OR (` +
		`length(` + q + `) = 19 AND ` + q + ` <= '9223372036854775807'))`
}

func tableName(schema, table string) string { return pgx.Identifier{schema, table}.Sanitize() }

func columnSet(t Table) map[string]bool {
	m := make(map[string]bool, len(t.Columns))
	for _, col := range t.Columns {
		m[col.Name] = true
	}
	return m
}

func sliceContains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func countWhere(ctx context.Context, tx pgx.Tx, report *Report, level, rule, table, query string) error {
	var n int64
	if err := tx.QueryRow(ctx, query).Scan(&n); err != nil {
		return fmt.Errorf("probe %s: %w", rule, err)
	}
	add(report, level, rule, table, n)
	return nil
}

func auditVersions(ctx context.Context, tx pgx.Tx, schema string, report *Report) error {
	q := func(name string) string { return tableName(schema, "usermodel_"+name) }
	checks := []struct{ level, rule, table, sql string }{
		{"L2", "outbox_subject_owner", "usermodel_outbox", `SELECT count(*) FROM ` + q("outbox") + ` o WHERE NOT EXISTS (SELECT 1 FROM ` + q("subject_state") + ` s WHERE (s.authority_id,s.tenant_id,s.subject_id)=(o.authority_id,o.tenant_id,o.subject_id))`},
		{"L2", "watermark_subject_owner", "usermodel_watermarks", `SELECT count(*) FROM ` + q("watermarks") + ` w WHERE NOT EXISTS (SELECT 1 FROM ` + q("subject_state") + ` s WHERE (s.authority_id,s.tenant_id,s.subject_id)=(w.authority_id,w.tenant_id,w.subject_id))`},
		{"L2", "binding_subject_owner", "usermodel_subject_bindings", `SELECT count(*) FROM ` + q("subject_bindings") + ` b WHERE NOT EXISTS (SELECT 1 FROM ` + q("subject_state") + ` s WHERE (s.authority_id,s.tenant_id,s.subject_id)=(b.authority_id,b.tenant_id,b.subject_id))`},
		{"L2", "bound_unmapped_owner", "usermodel_unmapped_events", `SELECT count(*) FROM ` + q("unmapped_events") + ` u WHERE u.bound_subject_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + q("subject_state") + ` s WHERE (s.authority_id,s.tenant_id,s.subject_id)=(u.authority_id,u.tenant_id,u.bound_subject_id) AND s.state_version>=u.bound_version)`},
		{"L2", "bound_unmapped_binding_disagreement", "usermodel_unmapped_events", `SELECT count(*) FROM ` + q("unmapped_events") + ` u WHERE u.bound_subject_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + q("subject_bindings") + ` b WHERE (b.authority_id,b.tenant_id,b.external_subject_id,b.subject_id)=(u.authority_id,u.tenant_id,u.external_subject_id,u.bound_subject_id))`},
		{"L2", "event_version_watermark", "usermodel_events", `SELECT count(*) FROM ` + q("events") + ` e LEFT JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE (e.status='accepted') <> (e.accepted_version IS NOT NULL) OR e.initial_version > s.state_version OR e.accepted_version > s.state_version OR e.accepted_version < e.initial_version`},
		{"L2", "active_fact_version_or_status", "usermodel_active_facts", `SELECT count(*) FROM ` + q("active_facts") + ` a JOIN ` + q("events") + ` e USING(authority_id,tenant_id,subject_id,producer,event_id) JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE e.status<>'accepted' OR e.action='retract' OR a.activated_version<>e.accepted_version OR a.activated_version>s.state_version`},
		{"L2", "missing_current_active_fact", "usermodel_events", `SELECT count(*) FROM ` + q("events") + ` e WHERE e.status='accepted' AND e.action<>'retract' AND NOT EXISTS (SELECT 1 FROM ` + q("events") + ` successor WHERE (successor.authority_id,successor.tenant_id,successor.subject_id,successor.supersedes_producer,successor.supersedes_event_id)=(e.authority_id,e.tenant_id,e.subject_id,e.producer,e.event_id) AND successor.status='accepted') AND NOT EXISTS (SELECT 1 FROM ` + q("active_facts") + ` a WHERE (a.authority_id,a.tenant_id,a.subject_id,a.producer,a.event_id)=(e.authority_id,e.tenant_id,e.subject_id,e.producer,e.event_id))`},
		{"L2", "attribution_version_watermark", "usermodel_attributions", `SELECT count(*) FROM ` + q("attributions") + ` a JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE a.linked_version>s.state_version OR a.revoked_version>s.state_version OR a.revoked_version<=a.linked_version`},
		{"L2", "outbox_version_gap", "usermodel_outbox", `SELECT count(*) FROM ` + q("subject_state") + ` s LEFT JOIN (SELECT authority_id,tenant_id,subject_id,count(*) AS n,min(state_version) AS min_v,max(state_version) AS max_v FROM ` + q("outbox") + ` GROUP BY 1,2,3) o USING(authority_id,tenant_id,subject_id) WHERE s.state_version<>coalesce(o.n,0) OR s.state_version<>coalesce(o.max_v,0) OR (s.state_version>0 AND o.min_v IS DISTINCT FROM 1)`},
		{"L2", "watermark_position", "usermodel_watermarks", `SELECT count(*) FROM ` + q("watermarks") + ` w WHERE w.contiguous_sequence<0 OR w.max_seen_sequence<w.contiguous_sequence OR w.max_seen_sequence<>coalesce((SELECT max(e.source_sequence) FROM ` + q("events") + ` e WHERE (e.authority_id,e.tenant_id,e.subject_id,e.producer,e.source_partition)=(w.authority_id,w.tenant_id,w.subject_id,w.producer,w.source_partition)),0) OR w.contiguous_sequence<>(SELECT count(*) FROM ` + q("events") + ` e WHERE (e.authority_id,e.tenant_id,e.subject_id,e.producer,e.source_partition)=(w.authority_id,w.tenant_id,w.subject_id,w.producer,w.source_partition) AND e.source_sequence BETWEEN 1 AND w.contiguous_sequence AND e.status='accepted')`},
		{"L2", "missing_source_watermark", "usermodel_events", `SELECT count(*) FROM ` + q("events") + ` e WHERE e.source_sequence IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + q("watermarks") + ` w WHERE (w.authority_id,w.tenant_id,w.subject_id,w.producer,w.source_partition)=(e.authority_id,e.tenant_id,e.subject_id,e.producer,e.source_partition))`},
		{"L2", "coverage_event_version", "usermodel_coverage_event", `SELECT count(*) FROM ` + q("coverage_event") + ` c JOIN ` + q("events") + ` e USING(authority_id,tenant_id,subject_id,producer,event_id) JOIN ` + q("coverage_prefix") + ` p USING(manifest_sha256) WHERE e.status<>'accepted' OR c.accepted_version IS DISTINCT FROM e.accepted_version OR c.source_offset>p.through_offset`},
		{"L2", "coverage_subject_count", "usermodel_coverage_subject", `SELECT count(*) FROM ` + q("coverage_subject") + ` c WHERE c.event_count<>(SELECT count(*) FROM ` + q("coverage_event") + ` e WHERE (e.manifest_sha256,e.authority_id,e.tenant_id,e.subject_id)=(c.manifest_sha256,c.authority_id,c.tenant_id,c.subject_id))`},
		{"L2", "ontology_projection_future", "usermodel_ontology_projections", `SELECT count(*) FROM ` + q("ontology_projections") + ` p JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE p.state_version>s.state_version`},
		{"L3", "ontology_projection_stale", "usermodel_ontology_projections", `SELECT count(*) FROM ` + q("ontology_projections") + ` p JOIN ` + q("ontology_heads") + ` h USING(authority_id,tenant_id) JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE p.definition_version<>h.definition_version OR p.state_version<s.state_version`},
		{"L3", "ontology_projection_missing", "usermodel_subject_state", `SELECT count(*) FROM ` + q("subject_state") + ` s JOIN ` + q("ontology_heads") + ` h USING(authority_id,tenant_id) WHERE NOT EXISTS (SELECT 1 FROM ` + q("ontology_projections") + ` p WHERE (p.authority_id,p.tenant_id,p.subject_id)=(s.authority_id,s.tenant_id,s.subject_id))`},
		{"L2", "feature_snapshot_future", "usermodel_feature_snapshots", `SELECT count(*) FROM ` + q("feature_snapshots") + ` f JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE f.state_version>s.state_version`},
		{"L2", "feature_baseline_revision_orphan", "usermodel_feature_snapshots", `SELECT count(*) FROM ` + q("feature_snapshots") + ` f WHERE f.baseline_revision IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ` + q("feature_baselines") + ` b WHERE (b.authority_id,b.tenant_id,b.subject_id,b.revision)=(f.authority_id,f.tenant_id,f.subject_id,f.baseline_revision))`},
		{"L2", "serving_pointer_pair_owner", "usermodel_serving_pointers", `SELECT count(*) FROM ` + q("serving_pointers") + ` p JOIN ` + q("serving_bundles") + ` b USING(authority_id,tenant_id,subject_id,bundle_id) WHERE p.pair_id<>b.pair_id`},
		{"L2", "covered_baseline_future", "usermodel_covered_baselines_v2", `SELECT count(*) FROM ` + q("covered_baselines_v2") + ` b JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE b.input_state_version>s.state_version`},
		{"L2", "covered_snapshot_future", "usermodel_covered_snapshots_v2", `SELECT count(*) FROM ` + q("covered_snapshots_v2") + ` c JOIN ` + q("subject_state") + ` s USING(authority_id,tenant_id,subject_id) WHERE c.input_state_version>s.state_version`},
		{"L2", "covered_snapshot_baseline_owner", "usermodel_covered_snapshots_v2", `SELECT count(*) FROM ` + q("covered_snapshots_v2") + ` c JOIN ` + q("covered_baselines_v2") + ` b ON (b.authority_id,b.tenant_id,b.subject_id,b.revision)=(c.authority_id,c.tenant_id,c.subject_id,c.baseline_revision) WHERE c.baseline_artifact_sha256<>b.artifact_sha256 OR c.prefix_manifest_sha256<>b.prefix_manifest_sha256 OR c.subject_receipt_sha256<>b.subject_receipt_sha256`},
	}
	for _, check := range checks {
		if err := countWhere(ctx, tx, report, check.level, check.rule, check.table, check.sql); err != nil {
			return err
		}
	}
	return nil
}
