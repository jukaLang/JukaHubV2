// SQLite support for patch logs and performance consumption.
//
// SQLite is chosen because it is a single, cross-platform C library with a
// well-tested Go binding that works well in an embedded desktop application.
package main

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
	"github.com/tidwall/gjson"
)

// PatchDB exposes the storage layer used by the perfor...(truncated)
