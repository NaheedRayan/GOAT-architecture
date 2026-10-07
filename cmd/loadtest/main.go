// Command loadtest hammers a running store with concurrent shoppers and prints
// latency percentiles and error rates. It only reads public pages, so it is safe
// to point at staging:
//
//	go run ./cmd/loadtest -base http://localhost:8080 -c 50 -d 20s
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"
)

func main() {
	base := flag.String("base", "http://localhost:8080", "store URL")
	conc := flag.Int("c", 20, "concurrent shoppers")
	dur := flag.Duration("d", 10*time.Second, "test duration")
	maxP95 := flag.Duration("max-p95", 0, "exit non-zero when p95 exceeds this (0 = no limit)")
	flag.Parse()

	client := &http.Client{Timeout: 15 * time.Second}
	body, err := get(client, *base+"/products")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot load the shop:", err)
		os.Exit(1)
	}
	slugs := regexp.MustCompile(`href="(/products/[^"?#]+)"`).FindAllStringSubmatch(body, -1)
	paths := []string{"/", "/products", "/products?q=a", "/products?sort=price_asc", "/sitemap.xml", "/healthz"}
	for _, m := range slugs {
		paths = append(paths, m[1])
	}

	var (
		mu     sync.Mutex
		lat    []time.Duration
		errs   int
		bad    int
		stop   = time.Now().Add(*dur)
		wg     sync.WaitGroup
		perURL = map[string]int{}
	)
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			for time.Now().Before(stop) {
				p := paths[rng.Intn(len(paths))]
				t := time.Now()
				res, err := client.Get(*base + p)
				d := time.Since(t)
				mu.Lock()
				switch {
				case err != nil:
					errs++
				default:
					_, _ = io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode >= 500 {
						bad++
					}
					lat = append(lat, d)
					perURL[p]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(lat) == 0 {
		fmt.Println("no successful requests")
		os.Exit(1)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration { return lat[min(len(lat)-1, int(float64(len(lat))*p))] }
	fmt.Printf("requests: %d (%.0f/s)  transport errors: %d  5xx: %d\n", len(lat), float64(len(lat))/dur.Seconds(), errs, bad)
	fmt.Printf("latency: p50=%v p90=%v p95=%v p99=%v max=%v\n", pct(.5), pct(.9), pct(.95), pct(.99), lat[len(lat)-1])
	if errs > 0 || bad > 0 || (*maxP95 > 0 && pct(.95) > *maxP95) {
		os.Exit(1)
	}
}

func get(c *http.Client, u string) (string, error) {
	res, err := c.Get(u)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return string(b), err
}
