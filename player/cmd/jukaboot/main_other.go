//go:build !linux

package main

import "os"

// Off Linux jukaboot is only the tool: "jukaboot plan DIR" reads a board
// folder's /jukahub files, "jukaboot initcalls FILE" a saved kernel log.
func main() {
	os.Exit(runTool(os.Args[1:]))
}
