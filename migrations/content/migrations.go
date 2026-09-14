// Package content exposes the explicit content-domain migration to deployment
// and isolated acceptance runners. Constructing a service never applies DDL.
package content

import _ "embed"

//go:embed 001_builds.sql
var SQL string
