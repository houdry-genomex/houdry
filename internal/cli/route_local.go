package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"houdry/internal/modelruntime"
	"houdry/internal/routerchat"
	"houdry/internal/routing"
)

// defaultOllamaURL is the loopback Ollama daemon. Local routing is a
// single-machine tool by design: it never reaches beyond this host.
const defaultOllamaURL = "http://127.0.0.1:11434"

// localRouteEnv is everything one prompt needs: the live model inventory as a
// one-node cluster view plus the catalog derived from it.
type localRouteEnv struct {
	runtime *modelruntime.Ollama
	catalog []routing.CatalogEntry
	node    routing.NodeView
}

func buildLocalRouteEnv(ctx context.Context, ollamaURL string) (*localRouteEnv, error) {
	rt := modelruntime.NewOllama(ollamaURL)
	if !rt.Available(ctx) {
		return nil, fmt.Errorf("no Ollama daemon reachable at %s (start Ollama first)", ollamaURL)
	}
	models, err := rt.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list local models: %w", err)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Ollama at %s has no models; pull one first (e.g. `ollama pull deepseek-r1:14b`)", ollamaURL)
	}
	return &localRouteEnv{
		runtime: rt,
		catalog: routing.CatalogFromModels(models),
		node: routing.NodeView{
			NodeID:        "local",
			Host:          strings.TrimPrefix(strings.TrimPrefix(ollamaURL, "http://"), "https://"),
			Status:        "READY",
			ModelRuntimes: []string{"ollama"},
			Models:        models,
		},
	}, nil
}

func (env *localRouteEnv) route(prompt string) routing.Decision {
	return routing.Route(routing.RouteRequest{
		Prompt:         prompt,
		Catalog:        env.catalog,
		Nodes:          []routing.NodeView{env.node},
		PreferLoaded:   true,
		RequirePresent: true,
	})
}

// runLocalRoute routes one prompt against the local Ollama daemon and, with
// run=true, executes the winning model and prints its answer.
func runLocalRoute(ctx context.Context, ollamaURL, prompt string, run, asJSON bool) error {
	env, err := buildLocalRouteEnv(ctx, ollamaURL)
	if err != nil {
		return err
	}
	dec := env.route(prompt)
	if asJSON {
		return encodeJSON(os.Stdout, map[string]any{"decision": dec})
	}
	printDecision(dec, env)
	if !run || dec.Selected == nil {
		return nil
	}
	return runSelected(ctx, env, dec, prompt)
}

// runRouteInteractive is the router test bench: read a prompt, show the full
// decision (profile, score, ranked candidates), optionally execute — repeat.
func runRouteInteractive(ctx context.Context, ollamaURL string, run bool) error {
	env, err := buildLocalRouteEnv(ctx, ollamaURL)
	if err != nil {
		return err
	}

	fmt.Printf("Houdry router test bench — %d local model(s) on %s\n", len(env.node.Models), ollamaURL)
	for _, e := range env.catalog {
		fmt.Printf("  · %-28s tier=%-6s caps=%s\n", e.Ref(), e.MaxComplexity, strings.Join(e.Capabilities, ","))
	}
	if run {
		fmt.Println("Mode: route + RUN the selected model. Type a prompt, or 'exit'.")
	} else {
		fmt.Println("Mode: route only (add --run to execute). Type a prompt, or 'exit'.")
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print("\nprompt> ")
		if !scanner.Scan() {
			fmt.Println()
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		dec := env.route(line)
		printDecision(dec, env)
		if run && dec.Selected != nil {
			if err := runSelected(ctx, env, dec, line); err != nil {
				fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
			}
		}
	}
}

func printDecision(dec routing.Decision, env *localRouteEnv) {
	p := dec.Profile
	fmt.Printf("── analysis ────────────────────────────────\n")
	fmt.Printf("modality=%s  complexity=%s  score=%d/100  caps=%s\n",
		p.Modality, p.Complexity, p.Score, strings.Join(p.Capabilities, ","))
	for _, h := range p.Hints {
		fmt.Printf("  • %s\n", h)
	}
	fmt.Printf("── candidates ──────────────────────────────\n")
	if len(dec.Candidates) == 0 {
		fmt.Printf("  (none) %s\n", dec.Message)
		return
	}
	for i, c := range dec.Candidates {
		marker := "  "
		if dec.Selected != nil && c.Entry.Ref() == dec.Selected.Entry.Ref() && c.NodeID == dec.Selected.NodeID {
			marker = "▶ "
		}
		state := "present"
		if c.Loaded {
			state = "loaded"
		}
		fmt.Printf("%s%d. %-28s score=%-4d %-8s %s\n",
			marker, i+1, c.Entry.Ref(), c.Score, state, strings.Join(c.Reasons, "; "))
	}
	if dec.Selected != nil {
		fmt.Printf("→ routed to %s\n", dec.Selected.Entry.Ref())
	} else if dec.Message != "" {
		fmt.Printf("→ %s\n", dec.Message)
	}
}

func runSelected(ctx context.Context, env *localRouteEnv, dec routing.Decision, prompt string) error {
	sel := dec.Selected
	fmt.Printf("── running %s ", sel.Entry.Ref())
	if !sel.Loaded {
		fmt.Printf("(cold start: loading weights) ")
	}
	fmt.Println("──")
	started := time.Now()
	res, err := env.runtime.Infer(ctx, modelruntime.InferRequest{
		Name:   sel.Entry.Name,
		Tag:    sel.Entry.Tag,
		Prompt: prompt,
	})
	if err != nil {
		return err
	}
	answer, thought := routerchat.SplitThink(res.Text)
	if thought != "" {
		fmt.Printf("(model thought for %d chars — hidden; answer below)\n", len(thought))
	}
	fmt.Println(strings.TrimSpace(answer))
	fmt.Printf("── done in %s (load %dms · generate %dms) ──\n",
		time.Since(started).Round(time.Millisecond), res.LoadMS, res.GenerateMS)
	return nil
}
