// HTTP load test against a running loamss daemon. Measures end-to-end
// latency for tools/call (memory.show), the full request path:
// HTTP framing → bearer auth → JSON-RPC dispatch → permission.Check
// → tool handler → memory adapter Get → JSON-RPC response.
//
// Usage:
//
//	# in one terminal:
//	./bin/loamss --config <cfg> start
//
//	# in another:
//	go run ./scripts/loadtest_mcp.go \
//	    --addr=http://127.0.0.1:7777 \
//	    --token=<bearer> \
//	    --id=<seeded-memory-id> \
//	    --concurrency=8 --requests=2000
//
// Output: p50/p95/p99 latencies, qps, error counts.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "http://127.0.0.1:7777", "daemon base URL")
	token := flag.String("token", "", "bearer token (required)")
	id := flag.String("id", "mem-seed-0", "memory entry id to fetch")
	concurrency := flag.Int("concurrency", 8, "concurrent workers")
	requests := flag.Int("requests", 1000, "total requests across all workers")
	method := flag.String("method", "memory.show", "tool to call (memory.show | client.info)")
	flag.Parse()
	if *token == "" {
		fmt.Fprintln(os.Stderr, "--token is required (run `loamss client pair complete` to get one)")
		os.Exit(1)
	}

	// Precompose the request body.
	args := map[string]any{"id": *id}
	if *method == "client.info" {
		args = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      *method,
			"arguments": args,
		},
	})

	// HTTP client tuned for high concurrency to localhost.
	transport := &http.Transport{
		MaxIdleConns:        *concurrency * 2,
		MaxIdleConnsPerHost: *concurrency * 2,
		MaxConnsPerHost:     *concurrency * 2,
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	perWorker := *requests / *concurrency
	total := perWorker * *concurrency

	latencies := make([]int64, 0, total)
	var latMu sync.Mutex
	var errors atomic.Int64
	var done atomic.Int64

	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				t := time.Now()
				req, _ := http.NewRequest(http.MethodPost, *addr+"/mcp", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+*token)
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					errors.Add(1)
					continue
				}
				bodyBytes, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errors.Add(1)
					continue
				}
				// Verify the response is shaped correctly. Bad
				// responses (RPC error, missing fields) count as
				// errors so the qps headline doesn't lie.
				var probe struct {
					Error  *struct{}      `json:"error"`
					Result map[string]any `json:"result"`
				}
				if err := json.Unmarshal(bodyBytes, &probe); err != nil {
					errors.Add(1)
					continue
				}
				if probe.Error != nil || probe.Result == nil {
					errors.Add(1)
					continue
				}
				lat := time.Since(t).Nanoseconds()
				latMu.Lock()
				latencies = append(latencies, lat)
				latMu.Unlock()
				done.Add(1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if len(latencies) == 0 {
		fmt.Println("all requests failed")
		os.Exit(1)
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p := func(pct float64) int64 {
		idx := int(float64(len(latencies)-1) * pct)
		return latencies[idx]
	}

	fmt.Printf("method:       %s\n", *method)
	fmt.Printf("requests:     %d (errors: %d)\n", done.Load(), errors.Load())
	fmt.Printf("concurrency:  %d\n", *concurrency)
	fmt.Printf("elapsed:      %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("qps:          %.0f\n", float64(done.Load())/elapsed.Seconds())
	fmt.Printf("p50:          %s\n", time.Duration(p(0.5)))
	fmt.Printf("p95:          %s\n", time.Duration(p(0.95)))
	fmt.Printf("p99:          %s\n", time.Duration(p(0.99)))
	fmt.Printf("max:          %s\n", time.Duration(latencies[len(latencies)-1]))
}
