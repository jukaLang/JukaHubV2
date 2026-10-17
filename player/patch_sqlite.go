// SQLite support for patch logs and performance consumption.
//
// SQLite is chosen because it is a single, cross-platform C library with a
// well-tested Go binding that works well in an embedded desktop application.
//
// (Design note carried over from the "Patch" commit; the unused
// database/sql + go-sqlite3 + gjson imports were dropped so the package
// compiles. Wire them up here when the SQLite log store lands.)
package main
