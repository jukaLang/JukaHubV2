package main

// playtime_export.go — the Playtime screen's export (LIB-13).
//
// muOS's playtime screen exports what it measured as a CSV and an HTML page;
// this writes the same two files into the folder a backup already goes to
// (the card's "JukaHub Backups" folder on a handheld, Downloads on a PC), so a
// spreadsheet or a browser can read the history without JukaHub. The figures
// are the lifetime ledger joined with the session log: one line per game with
// its total time, its console, its content hash and the date of its last
// session. Nothing is written into the data folder or the user config.

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	playtimeExportCSVName  = "playtime.csv"
	playtimeExportHTMLName = "playtime.html"
)

// playtimeExportCSV renders the rows as the export's CSV: a header and one line
// per game, seconds as an integer and hours to two decimals so a spreadsheet
// can sum and read them.
func playtimeExportCSV(rows []playtimeExportRow) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"system", "title", "hash", "seconds", "hours", "last_played"})
	for _, r := range rows {
		_ = w.Write([]string{
			r.System,
			r.Title,
			r.Hash,
			strconv.FormatInt(r.Seconds, 10),
			strconv.FormatFloat(float64(r.Seconds)/3600, 'f', 2, 64),
			r.LastPlayed,
		})
	}
	w.Flush()
	return buf.Bytes()
}

// playtimeExportHTML renders the same rows as one page a browser can open:
// the totals, then the table. Every value is escaped, since a game's title is
// a file name and may hold anything.
func playtimeExportHTML(rows []playtimeExportRow, now string) []byte {
	var total int64
	for _, r := range rows {
		total += r.Seconds
	}
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>JukaHub playtime</title>")
	b.WriteString("<style>body{font:15px/1.5 system-ui,sans-serif;margin:2rem;color:#222}" +
		"table{border-collapse:collapse;width:100%}th,td{text-align:left;padding:.35rem .6rem;" +
		"border-bottom:1px solid #ddd}th{font-weight:600}td.n{text-align:right;font-variant-numeric:tabular-nums}</style>")
	b.WriteString("<h1>Playtime</h1><p>")
	b.WriteString(html.EscapeString(fmt.Sprintf("%d games · %s in total · exported %s", len(rows), formatPlaytime(total), now)))
	b.WriteString("</p><table><tr><th>Game</th><th>Console</th><th>Time</th><th class=\"n\">Hours</th><th>Last played</th></tr>")
	for _, r := range rows {
		b.WriteString("<tr><td>")
		b.WriteString(html.EscapeString(r.Title))
		b.WriteString("</td><td>")
		b.WriteString(html.EscapeString(r.System))
		b.WriteString("</td><td>")
		b.WriteString(html.EscapeString(formatPlaytime(r.Seconds)))
		b.WriteString("</td><td class=\"n\">")
		b.WriteString(strconv.FormatFloat(float64(r.Seconds)/3600, 'f', 2, 64))
		b.WriteString("</td><td>")
		b.WriteString(html.EscapeString(r.LastPlayed))
		b.WriteString("</td></tr>")
	}
	b.WriteString("</table>")
	return []byte(b.String())
}

// playtimeExportFiles writes both files into dir and returns their paths.
func playtimeExportFiles(dir string, rows []playtimeExportRow, now string) (csvPath, htmlPath string, err error) {
	if strings.TrimSpace(dir) == "" {
		return "", "", fmt.Errorf("no folder to write the playtime export to")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	csvPath = filepath.Join(dir, playtimeExportCSVName)
	htmlPath = filepath.Join(dir, playtimeExportHTMLName)
	if err := AtomicWrite(csvPath, playtimeExportCSV(rows), 0o644); err != nil {
		return "", "", err
	}
	if err := AtomicWrite(htmlPath, playtimeExportHTML(rows, now), 0o644); err != nil {
		return "", "", err
	}
	return csvPath, htmlPath, nil
}

// playtimeExportToCard is the screen's action: write the export where a backup
// goes and say where it landed.
func playtimeExportToCard() bool {
	dir := bkupExportDir()
	if dir == "" {
		if d, err := P().DataDir(); err == nil {
			dir = d
		}
	}
	rows := playtimeExportRows(playtime.data.Games, playtime.sessions)
	csvPath, _, err := playtimeExportFiles(dir, rows, timeNowExportLabel())
	if err != nil {
		logWarn("playtime", "cannot write the playtime export: %v", err)
		ShowToast("Could not write the playtime export", ToastKindWarning)
		return true
	}
	ShowToast("Playtime saved · "+filepath.Base(csvPath), ToastKindSuccess)
	return true
}

// timeNowExportLabel is the export's date line, local and readable.
func timeNowExportLabel() string {
	return gameClock().Format("2006-01-02 15:04")
}
