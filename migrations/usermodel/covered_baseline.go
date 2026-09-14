package usermodel

import _ "embed"

// CoveredBaselineSQL is an explicit v2 migration; Store constructors do not
// apply it or alter legacy feature heads and ServingBundle tables.
//
//go:embed 005_covered_baseline.sql
var CoveredBaselineSQL string
