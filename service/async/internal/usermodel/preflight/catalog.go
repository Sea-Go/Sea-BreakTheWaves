package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Catalog is the physical PostgreSQL contract, including columns and the keys
// that the seven frozen usermodel migrations actually create.
type Catalog struct {
	Tables []Table `json:"tables"`
}

type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Constraints []Constraint `json:"constraints"`
	Unique      []UniqueKey  `json:"unique"`
}

type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	NotNull  bool   `json:"not_null"`
	Identity string `json:"identity,omitempty"`
	Default  string `json:"default,omitempty"`
}

type Constraint struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Columns    []string `json:"columns"`
	RefTable   string   `json:"ref_table,omitempty"`
	RefColumns []string `json:"ref_columns,omitempty"`
	Definition string   `json:"definition"`
}

type UniqueKey struct {
	Name      string   `json:"name"`
	Columns   []string `json:"columns"`
	Predicate string   `json:"predicate,omitempty"`
}

var migrationTables = []string{
	"usermodel_subject_state", "usermodel_events", "usermodel_unmapped_events",
	"usermodel_subject_bindings", "usermodel_active_facts", "usermodel_attributions",
	"usermodel_watermarks", "usermodel_outbox", "usermodel_coverage_prefix",
	"usermodel_coverage_event", "usermodel_coverage_subject",
	"usermodel_ontology_definitions", "usermodel_ontology_heads",
	"usermodel_ontology_projections", "usermodel_feature_baselines",
	"usermodel_feature_heads", "usermodel_feature_snapshots",
	"usermodel_feature_snapshot_versions", "usermodel_serving_bundles",
	"usermodel_serving_pointers", "usermodel_covered_baselines_v2",
	"usermodel_covered_snapshots_v2", "usermodel_covered_bundle_candidates_v2",
}

func readCatalog(ctx context.Context, tx pgx.Tx, schema string) (Catalog, error) {
	var c Catalog
	rows, err := tx.Query(ctx, `SELECT relname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relkind='r' AND c.relname=ANY($2::text[])
		ORDER BY relname`, schema, migrationTables)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Name); err != nil {
			rows.Close()
			return c, err
		}
		c.Tables = append(c.Tables, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	for i := range c.Tables {
		t := &c.Tables[i]
		if err := readColumns(ctx, tx, schema, t); err != nil {
			return c, err
		}
		if err := readConstraints(ctx, tx, schema, t); err != nil {
			return c, err
		}
		if err := readUnique(ctx, tx, schema, t); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Inspect is for generating/reviewing a structural oracle from an isolated
// migration fixture. It uses the same read-only snapshot as the audit.
func Inspect(ctx context.Context, conn *pgx.Conn, schema string) (Catalog, error) {
	if schema == "" {
		return Catalog{}, fmt.Errorf("schema is required")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Catalog{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL search_path TO `+pgx.Identifier{schema}.Sanitize()); err != nil {
		return Catalog{}, err
	}
	return readCatalog(ctx, tx, schema)
}

func readColumns(ctx context.Context, tx pgx.Tx, schema string, t *Table) error {
	rows, err := tx.Query(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,
		coalesce(a.attidentity::text,''),coalesce(pg_catalog.pg_get_expr(d.adbin,d.adrelid),'')
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
		WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped
		ORDER BY a.attnum`, schema, t.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var col Column
		if err := rows.Scan(&col.Name, &col.Type, &col.NotNull, &col.Identity, &col.Default); err != nil {
			return err
		}
		t.Columns = append(t.Columns, col)
	}
	return rows.Err()
}

func readConstraints(ctx context.Context, tx pgx.Tx, schema string, t *Table) error {
	rows, err := tx.Query(ctx, `SELECT x.conname,x.contype::text,
		ARRAY(SELECT a.attname FROM unnest(x.conkey) WITH ORDINALITY k(num,ord)
			JOIN pg_catalog.pg_attribute a ON a.attrelid=x.conrelid AND a.attnum=k.num ORDER BY k.ord),
		coalesce(r.relname,''),
		ARRAY(SELECT a.attname FROM unnest(x.confkey) WITH ORDINALITY k(num,ord)
			JOIN pg_catalog.pg_attribute a ON a.attrelid=x.confrelid AND a.attnum=k.num ORDER BY k.ord),
		pg_catalog.pg_get_constraintdef(x.oid,false)
		FROM pg_catalog.pg_constraint x
		JOIN pg_catalog.pg_class c ON c.oid=x.conrelid
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		LEFT JOIN pg_catalog.pg_class r ON r.oid=x.confrelid
		WHERE n.nspname=$1 AND c.relname=$2 ORDER BY x.conname`, schema, t.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var con Constraint
		if err := rows.Scan(&con.Name, &con.Kind, &con.Columns, &con.RefTable, &con.RefColumns, &con.Definition); err != nil {
			return err
		}
		if con.Columns == nil {
			con.Columns = []string{}
		}
		if con.RefColumns == nil {
			con.RefColumns = []string{}
		}
		t.Constraints = append(t.Constraints, con)
	}
	return rows.Err()
}

func readUnique(ctx context.Context, tx pgx.Tx, schema string, t *Table) error {
	rows, err := tx.Query(ctx, `SELECT idx.relname,
		ARRAY(SELECT a.attname FROM unnest(i.indkey::int2[]) WITH ORDINALITY k(num,ord)
			JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.num
			WHERE k.ord<=i.indnkeyatts ORDER BY k.ord),
		coalesce(pg_catalog.pg_get_expr(i.indpred,i.indrelid),'')
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class idx ON idx.oid=i.indexrelid
		JOIN pg_catalog.pg_class c ON c.oid=i.indrelid
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relname=$2 AND i.indisunique ORDER BY idx.relname`, schema, t.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key UniqueKey
		if err := rows.Scan(&key.Name, &key.Columns, &key.Predicate); err != nil {
			return err
		}
		if key.Columns == nil {
			key.Columns = []string{}
		}
		t.Unique = append(t.Unique, key)
	}
	return rows.Err()
}

func (c Catalog) SHA256() string {
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c Catalog) ByName() map[string]Table {
	m := make(map[string]Table, len(c.Tables))
	for _, t := range c.Tables {
		m[t.Name] = t
	}
	return m
}

func (c Catalog) ValidateNames() error {
	if len(c.Tables) != len(migrationTables) {
		return fmt.Errorf("catalog has %d of %d scoped tables", len(c.Tables), len(migrationTables))
	}
	want := append([]string(nil), migrationTables...)
	sort.Strings(want)
	for i, t := range c.Tables {
		if t.Name != want[i] {
			return fmt.Errorf("catalog scoped table mismatch")
		}
	}
	return nil
}
