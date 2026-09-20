// lua-lsp is a Language Server Protocol server for Lua over stdio.
package main

import (
	"flag"
	"log"
	"os"

	"github.com/tliron/commonlog"
	_ "github.com/tliron/commonlog/simple"

	"github.com/fqix/lua-devtools/internal/lsp"
)

func main() {
	debug := flag.Bool("debug", false, "log every message to stderr")
	// vscode-languageclient appends --stdio for the stdio transport; stdio is the only transport.
	flag.Bool("stdio", true, "serve over stdin/stdout (always on)")
	flag.Parse()

	// stdout is the protocol channel; logs go to stderr.
	log.SetOutput(os.Stderr)
	verbosity := 0
	if *debug {
		verbosity = 2
	}
	commonlog.Configure(verbosity, nil)

	if err := lsp.Run(*debug); err != nil {
		log.Fatalf("lsp server: %v", err)
	}
}
