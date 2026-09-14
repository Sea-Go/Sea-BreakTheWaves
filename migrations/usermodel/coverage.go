package usermodel

import _ "embed"

// CoverageSQL is an explicit v2 cache migration after SQL. Constructors do
// not execute it and old subject watermarks are left untouched.
//
//go:embed 002_coverage_verification.sql
var CoverageSQL string
