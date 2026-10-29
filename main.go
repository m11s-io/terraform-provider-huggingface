package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/m11s-io/terraform-provider-huggingface/internal/provider"
)

// version is set by release builds using -X main.version.
var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "Run with debugger support")
	flag.Parse()
	if err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address:         "registry.terraform.io/m11s-io/huggingface",
		ProtocolVersion: 6,
		Debug:           *debug,
	}); err != nil {
		log.Fatal(err)
	}
}
