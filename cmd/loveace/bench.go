package main

// bench: offline concurrency benchmark against the in-memory mock transport.
// Simulates N accounts doing login + a business operation, then reports
// throughput, latency percentiles, memory and goroutine counts.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/ops"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// benchTransport returns a shared login+business mock. A single MockTransport
// instance is concurrency-safe (it takes a lock per request), so one shared
// transport is realistic for measuring session-level contention.
func benchTransport() *transport.MockTransport {
	return testutilLoginTransport().
		OnRequest(transport.Post, "/main/academicInfo", transport.NewResponse(200, []byte(
			`[{"courseNum":10,"coursePas":1,"gpa":3.52,"courseNum_bxqyxd":2,"zxjxjhh":"2024-2025-1-1"}]`)))
}

func cmdBench(args []string) int {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sessions := fs.Int("sessions", 1000, "number of concurrent account sessions")
	operation := fs.String("ops", "jwc.academic_info", "operation each session executes")
	jsonOut := fs.Bool("json", false, "emit result as JSON to stdout")
	if err := fs.Parse(args); err != nil {
		return exitInvocation
	}

	startedAt := timeutil.NowMillis()
	tr := benchTransport()
	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)
	goroutinesBefore := runtime.NumGoroutine()

	var latencies struct {
		sync.Mutex
		slice []time.Duration
	}
	var mu = &latencies.Mutex
	var failures atomic.Int64
	var successes atomic.Int64

	var wg sync.WaitGroup
	benchStart := time.Now()
	for i := 0; i < *sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			account := fmt.Sprintf("bench-%06d", i)
			s := session.New(account, "gw", "school", tr)
			start := time.Now()
			_, err := ops.Dispatch(context.Background(), s, *operation, json.RawMessage(`{}`))
			elapsed := time.Since(start)
			if err != nil {
				failures.Add(1)
				return
			}
			successes.Add(1)
			mu.Lock()
			latencies.slice = append(latencies.slice, elapsed)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	wallTime := time.Since(benchStart)

	runtime.ReadMemStats(&memAfter)
	goroutinesAfter := runtime.NumGoroutine()

	mu.Lock()
	lats := make([]float64, len(latencies.slice))
	for i, d := range latencies.slice {
		lats[i] = float64(d.Microseconds()) / 1000.0
	}
	mu.Unlock()
	sort.Float64s(lats)

	percentile := func(p float64) float64 {
		if len(lats) == 0 {
			return 0
		}
		idx := int(float64(len(lats)-1) * p)
		return lats[idx]
	}

	heapDeltaMB := float64(memAfter.HeapAlloc)/1024/1024 - float64(memBefore.HeapAlloc)/1024/1024
	result := map[string]any{
		"sessions":          *sessions,
		"operation":         *operation,
		"successes":         successes.Load(),
		"failures":          failures.Load(),
		"wall_time_ms":      wallTime.Milliseconds(),
		"throughput_ops_s":  float64(*sessions) / wallTime.Seconds(),
		"p50_ms":            percentile(0.50),
		"p95_ms":            percentile(0.95),
		"p99_ms":            percentile(0.99),
		"heap_delta_mb":     heapDeltaMB,
		"goroutines_before": goroutinesBefore,
		"goroutines_after":  goroutinesAfter,
	}

	if *jsonOut {
		data, _ := json.Marshal(result)
		fmt.Println(string(data))
	} else {
		fmt.Fprintf(os.Stderr, "sessions=%d op=%s\n", *sessions, *operation)
		fmt.Fprintf(os.Stderr, "success=%d failures=%d wall=%v throughput=%.0f ops/s\n",
			successes.Load(), failures.Load(), wallTime.Round(time.Millisecond), result["throughput_ops_s"])
		fmt.Fprintf(os.Stderr, "latency p50=%.2fms p95=%.2fms p99=%.2fms\n",
			result["p50_ms"], result["p95_ms"], result["p99_ms"])
		fmt.Fprintf(os.Stderr, "heap delta=%.1fMB goroutines %d -> %d\n",
			heapDeltaMB, goroutinesBefore, goroutinesAfter)
	}
	emitSuccess("bench", startedAt, result)
	// Goroutine leak check: after all wg.Wait, counts should return to baseline.
	if goroutinesAfter > goroutinesBefore+*sessions+16 {
		return exitBusiness
	}
	return exitOK
}
