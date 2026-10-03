package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/irashack/terraform-provider-pocketid/internal/provider"
)

var (
	// These will be set by the goreleaser configuration
	// to appropriate values for the compiled binary (-X main.version=...).
	// A build without it reports the release it leads up to.
	version string = "3.0.0-dev"
)

func main() {
	silenceStandardLogger()

	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/irashack/pocketid",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

// silenceStandardLogger sends Go's standard logger to io.Discard before the
// provider serves. net/http's HTTP/1 client writes the content of an
// "unsolicited response" there (Transport readLoop, readLoopPeekFailLocked:
// "Unsolicited response received on idle HTTP channel starting with %q"),
// and a server can put the API key it received into such bytes. The client
// package keeps those bytes from ever being read (internal/client/dial.go);
// this closes the path at its sink as well. The provider and the plugin
// framework log through tflog/hclog, not the standard logger; the few
// standard-logger lines go-plugin writes for its own stream and broker
// errors are given up with it.
func silenceStandardLogger() {
	log.SetOutput(io.Discard)
}
