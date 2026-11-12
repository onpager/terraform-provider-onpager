package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/onpager/terraform-provider-onpager/internal/provider"
)

var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "Enable debugger attachment")
	showVersion := flag.Bool("version", false, "Print the provider version")
	flag.Parse()
	if *showVersion {
		fmt.Printf("terraform-provider-onpager %s\n", version)
		return
	}
	if err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: provider.Address,
		Debug:   *debug,
	}); err != nil {
		log.Fatal(err)
	}
}
