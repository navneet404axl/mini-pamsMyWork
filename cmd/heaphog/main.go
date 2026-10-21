// heaphog: a heap-heavy workload, standing in for YOLO inference.
//
// It allocates a large amount of ordinary process memory ("anon" memory in
// Linux terms: heap with no file behind it) and keeps touching every page in
// a loop, so all of it stays in active use. The kernel can only take this
// memory away by writing it out to swap, which is expensive, so by default
// it prefers to evict page cache instead.
//
// Usage:
//
//	heaphog -gb 3 -duration 60
//
// Output: one CSV line per second, "t,mpages_per_sec" (millions of pages
// touched per second), then "AVG_MPAGES_PER_SEC <n>". If the kernel starts
// swapping this process out, that number drops: that is the cost that a
// PAMS-style controller shifts onto the heap app when it protects the cache.
package main

import (
	"flag"
	"fmt"
	"sync/atomic"
	"time"
)

const (
	pageSize  = 4096
	chunkSize = 64 << 20 // allocate in 64 MB chunks instead of one giant slice
)

func main() {
	gb := flag.Int("gb", 3, "GB of heap memory to allocate and keep busy")
	duration := flag.Int("duration", 60, "how long to run, in seconds")
	warmup := flag.Int("warmup", 10, "seconds excluded from the average")
	flag.Parse()

	// Allocate the memory and write to every page once. Go (like C's malloc)
	// only gets real physical pages from the OS when they are first written.
	nChunks := (*gb << 30) / chunkSize
	chunks := make([][]byte, nChunks)
	for i := range chunks {
		chunks[i] = make([]byte, chunkSize)
		for p := 0; p < chunkSize; p += pageSize {
			chunks[i][p] = 1
		}
	}

	var touched atomic.Int64
	stop := make(chan struct{})
	done := make(chan struct{})

	// Keep every page in use by touching one byte per page, round and round.
	go func() {
		defer close(done)
		for {
			for _, c := range chunks {
				for p := 0; p < chunkSize; p += pageSize {
					c[p]++
				}
				touched.Add(chunkSize / pageSize)
				select {
				case <-stop:
					return
				default:
				}
			}
		}
	}()

	fmt.Println("t,mpages_per_sec")
	var total float64
	var counted int
	tick := time.NewTicker(time.Second)
	for t := 1; t <= *duration; t++ {
		<-tick.C
		m := float64(touched.Swap(0)) / 1e6
		fmt.Printf("%d,%.2f\n", t, m)
		if t > *warmup {
			total += m
			counted++
		}
	}
	close(stop)
	<-done

	if counted > 0 {
		fmt.Printf("AVG_MPAGES_PER_SEC %.2f\n", total/float64(counted))
	}
}
