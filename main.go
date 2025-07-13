package main

import (
	"log"

	"github.com/rancher/crd-swagger/pkg/cmd"
)

func main() {
	rootCmd := cmd.NewRootCommand()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
