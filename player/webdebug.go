package main

import "os"

// webFlexDebug turns on the parser's flex-group trace (FLEXDBG=1). It lives in
// the build, not in a test file: webpage.go and webpage_flow.go both read it,
// so a test-only declaration breaks `go build`.
var webFlexDebug = os.Getenv("FLEXDBG") != ""
