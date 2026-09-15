package usermodel

import _ "embed"

// SubjectRefV2SQL is an explicit, additive deployment candidate. It does not
// backfill old subjects or change any existing usermodel table or active head.
//
//go:embed 007_subjectref_v2_projection.sql
var SubjectRefV2SQL string
