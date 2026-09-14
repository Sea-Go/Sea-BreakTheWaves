package usermodel

import _ "embed"

// CoveredSnapshotSQL is the explicit WS08-D v2 migration. It creates no
// active head and does not modify legacy feature or ServingBundle storage.
//
//go:embed 006_covered_snapshot.sql
var CoveredSnapshotSQL string
