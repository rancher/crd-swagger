package main

import (
	"log"

	"github.com/KevinJoiner/crd-swagger/cmd"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatal(err)
	}
	_ = zap.ReplaceGlobals(logger)
	rootCmd := cmd.NewRootCommand()
	err = rootCmd.Execute()
	if err != nil {
		_ = logger.Sync()
		log.Fatal(err)
	}
	_ = logger.Sync()
}
