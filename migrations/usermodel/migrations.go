// Package usermodel exposes the explicit user fact migration to deployment and
// isolated acceptance. Opening the store never changes database schema.
package usermodel

import _ "embed"

//go:embed 001_facts.sql
var SQL string
