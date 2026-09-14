// Package content exposes the explicit content-domain migration to deployment
// and isolated acceptance runners. Constructing a service never applies DDL.
package content

import _ "embed"

//go:embed 001_builds.sql
var buildsSQL string

//go:embed 002_index_dispatch.sql
var dispatchSQL string

//go:embed 003_separate_dc_rtw_fences.sql
var separateFencesSQL string

// SQL applies the versioned, repeatable content migrations in order. Existing
// installations can run it again without dropping committed READY artifacts.
var SQL = buildsSQL + "\n" + dispatchSQL + "\n" + separateFencesSQL
