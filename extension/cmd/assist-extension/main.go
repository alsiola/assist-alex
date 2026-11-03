// Command assist-extension serves the assist extension, Alex's personal
// assistant, as an Open Conduit package over protocol version 1. The
// package-manifest and package-certification subcommands print the
// package's open-conduit-extension.json and certification.json.
package main

import (
	"os"

	assist "github.com/alsiola/assist-alex/extension"
	"github.com/open-conduit/open-conduit/protocol/sdk"
)

func main() {
	if sdk.PackageCommand(os.Args[1:], assist.Manifest(), assist.Certification(), os.Stdout) {
		return
	}
	_ = sdk.Serve(assist.New(assist.BuildOptions{}).Build())
}
