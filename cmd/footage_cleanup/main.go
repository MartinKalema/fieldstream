package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"fieldvideolab/internal/lab"
)

func main() {
	root := flag.String("root", ".", "project root")
	cutoff := flag.String("cutoff", "", "fixed footage cutoff in RFC3339")
	manifest := flag.String("manifest", "reports/footage-cleanup-manifest.json", "preview manifest relative to project root")
	execute := flag.Bool("execute", false, "execute the existing manifest; lab must be stopped")
	flag.Parse()
	abs, err := filepath.Abs(*root)
	if err != nil {
		fail(err)
	}
	p := lab.NewPaths(abs)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 5*time.Minute)
	defer deadline()
	name := filepath.Join(abs, *manifest)
	if !*execute {
		when, e := time.Parse(time.RFC3339, *cutoff)
		if e != nil {
			fail(e)
		}
		plan, e := lab.PrepareFootageCleanup(ctx, p, when)
		if e != nil {
			fail(e)
		}
		if e = lab.AtomicJSON(name, plan); e != nil {
			fail(e)
		}
		fmt.Println(plan.Summary())
		fmt.Println(name)
		return
	}
	data, err := os.ReadFile(name)
	if err != nil {
		fail(err)
	}
	var plan lab.FootageCleanup
	if err = json.Unmarshal(data, &plan); err != nil {
		fail(err)
	}
	result, executeErr := lab.ExecuteFootageCleanup(ctx, p, plan)
	resultPath := filepath.Join(abs, "reports", "footage-cleanup-result.json")
	if err = lab.AtomicJSON(resultPath, result); err != nil {
		fail(err)
	}
	fmt.Printf("Deleted %d local files, %d R2 objects; removed %d main catalog rows.\n%s\n", len(result.LocalDeleted), len(result.RemoteDeleted), result.CatalogDeleted, resultPath)
	if executeErr != nil {
		fail(executeErr)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
