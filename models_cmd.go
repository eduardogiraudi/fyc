package main

import (
	"context"
	"fmt"
	"time"

	"fuckyourcli/models"
)

// runModelsCommand implements `fuckyourcli models [update]`: it refreshes the
// local models.dev catalog (reasoning capabilities, limits, ...) used by
// /reasoning and by the reasoning translation tables.
func runModelsCommand(args []string) error {
	if len(args) == 0 {
		providers, count := models.Load().Stats()
		fmt.Printf("models catalog: %d providers, %d models (stored at %s)\n", providers, count, models.Path())
		fmt.Println("Run 'fuckyourcli models update' to fetch the latest from models.dev.")
		return nil
	}
	if args[0] != "update" {
		return fmt.Errorf("unknown subcommand %q (usage: fuckyourcli models update)", args[0])
	}

	fmt.Println("Fetching model catalog from models.dev...")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	providers, count, err := models.Update(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Catalog updated: %d providers, %d models.\n", providers, count)
	return nil
}
