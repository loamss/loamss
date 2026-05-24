// Smoke-test helper: seeds a few memory entries by going through the
// memory + model adapters directly. Lives under runtime/scripts/ as
// its own package so the smoke walkthrough in docs/runbook can
// reproduce a known-good memory.db state. Not built by the daemon
// pipeline; invoked as `go run ./scripts/smoke_seed.go <memory.db>`.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"
	"github.com/loamss/loamss/runtime/internal/adapter/model"
	_ "github.com/loamss/loamss/runtime/internal/adapter/model/dummy"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/smoke_seed.go <memory.db>")
		os.Exit(1)
	}
	ctx := context.Background()
	mem, err := memory.New("memory:sqlite")
	if err != nil {
		panic(err)
	}
	if err := mem.Init(ctx, map[string]any{"path": os.Args[1]}); err != nil {
		panic(err)
	}
	defer func() { _ = mem.Close(ctx) }()

	mdl, err := model.New("model:dummy")
	if err != nil {
		panic(err)
	}
	if err := mdl.Init(ctx, nil); err != nil {
		panic(err)
	}

	texts := []string{
		"sarah works at acme",
		"the contract is due tuesday",
		"favorite coffee is filter",
	}
	for i, txt := range texts {
		emb, err := mdl.Embed(ctx, model.EmbedRequest{ModelID: "dummy-embed", Text: txt})
		if err != nil {
			panic(err)
		}
		id := fmt.Sprintf("mem-seed-%d", i)
		if err := mem.Upsert(ctx, id, emb.Vector, map[string]any{"text": txt}); err != nil {
			panic(err)
		}
		fmt.Printf("seeded %s — %q\n", id, txt)
	}
}
