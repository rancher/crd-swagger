package main

import (
	"log"

	"github.com/KevinJoiner/crd-swagger/pkg/cmd"
)

func main() {
	rootCmd := cmd.NewRootCommand()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
