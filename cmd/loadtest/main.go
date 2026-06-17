// Command loadtest 对 lumina-relay 做阶梯压测。
//
// 用法：./loadtest -target http://localhost:8443
// 采集 QPS、P50/P95/P99、错误率，按并发档位输出。
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	statusCode int
	latency    time.Duration
	err        error
}

type stats struct {
	concurrency int
	total       int64
	errors      int64
	latencies   []time.Duration
	duration    time.Duration
}

func (s *stats) qps() float64 {
	if s.duration == 0 {
		return 0
	}
	return float64(s.total) / s.duration.Seconds()
}

func (s *stats) percentile(p float64) time.Duration {
	if len(s.latencies) == 0 {
		return 0
	}
	sort.Slice(s.latencies, func(i, j int) bool { return s.latencies[i] < s.latencies[j] })
	idx := int(float64(len(s.latencies)-1) * p)
	return s.latencies[idx]
}

func main() {
	target := flag.String("target", "http://localhost:8443", "目标服务地址")
	endpoint := flag.String("endpoint", "health", "端点：health | register")
	duration := flag.Duration("duration", 10*time.Second, "每档持续时长")
	flag.Parse()

	concurrencies := []int{10, 50, 100, 200}
	baseURL := *target

	fmt.Printf("压测目标：%s  端点：%s  每档时长：%s\n\n", baseURL, *endpoint, *duration)
	fmt.Printf("%-10s %12s %10s %10s %10s %10s %10s\n",
		"并发", "QPS", "总数", "错误", "P50", "P95", "P99")
	fmt.Println("──────────────────────────────────────────────────────────────────────────")

	for _, c := range concurrencies {
		s := bench(baseURL, *endpoint, c, *duration)
		fmt.Printf("%-10d %12.1f %10d %10d %10s %10s %10s\n",
			c, s.qps(), s.total, s.errors,
			fmtDur(s.percentile(0.5)),
			fmtDur(s.percentile(0.95)),
			fmtDur(s.percentile(0.99)),
		)
	}
	fmt.Println()
}

func bench(baseURL, endpoint string, concurrency int, duration time.Duration) *stats {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        concurrency * 2,
			MaxIdleConnsPerHost: concurrency * 2,
		},
	}

	deadline := time.Now().Add(duration)
	var total, errors int64
	latencies := make([]time.Duration, 0, 10000)
	var latMu sync.Mutex

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				start := time.Now()
				code, err := doReq(client, baseURL, endpoint)
				lat := time.Since(start)
				atomic.AddInt64(&total, 1)
				if err != nil || code >= 400 {
					atomic.AddInt64(&errors, 1)
				}
				latMu.Lock()
				latencies = append(latencies, lat)
				latMu.Unlock()
			}
		}()
	}
	wg.Wait()

	return &stats{
		concurrency: concurrency,
		total:       total,
		errors:      errors,
		latencies:   latencies,
		duration:    duration,
	}
}

func doReq(client *http.Client, baseURL, endpoint string) (int, error) {
	switch endpoint {
	case "health":
		resp, err := client.Get(baseURL + "/health")
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil

	case "register":
		// 每次用随机 hex 填充，确保唯一（account_id 由服务端 uuid 生成）
		body := map[string]string{
			"recoveryCodeHash": randHex(32),
			"dekSalt":          randHex(16),
			"dekNonce":         randHex(12),
			"dekCt":            randHex(48),
			"devicePubKey":     randHex(32),
			"deviceName":       "loadtest",
		}
		raw, _ := json.Marshal(body)
		resp, err := client.Post(baseURL+"/account/register", "application/json", bytes.NewReader(raw))
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	return 0, fmt.Errorf("unknown endpoint")
}

func randHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return fmt.Sprintf("%x", b)
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dμs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}
