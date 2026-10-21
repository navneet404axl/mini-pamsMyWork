// cacheapp: a page-cache-heavy workload, standing in for RocksDB.
//
// It does random 4 KB reads from a big file using normal (buffered) reads,
// so every read goes through the Linux page cache. If the file's pages are
// in the cache, a read is a fast memory copy. If they were evicted, the read
// must go to disk, which is far slower. That is exactly the effect behind
// Figure 1 of the paper.
//
// Usage:
//
//	cacheapp -create -file data.bin -size-gb 4            # make the data file once
//	cacheapp -file data.bin -size-gb 4 -duration 60       # run the workload
//
// Output: one CSV line per second, "t,ops_per_sec", then a final summary line
// "AVG_OPS_PER_SEC <n>" averaged over the seconds after the warm-up period.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const pageSize = 4096 // read size: one OS page, like a small key-value lookup

func main() {
	path := flag.String("file", "data.bin", "path of the data file")
	sizeGB := flag.Int("size-gb", 4, "size of the data file in GB")
	create := flag.Bool("create", false, "create the data file and exit")
	duration := flag.Int("duration", 60, "how long to run, in seconds")
	warmup := flag.Int("warmup", 10, "seconds excluded from the average (cache warming)")
	workers := flag.Int("workers", 4, "number of goroutines doing reads in parallel")
	triggerFile := flag.String("trigger-file", "", "if set, write 'emergency' here after -trigger-after seconds")
	triggerAfter := flag.Int("trigger-after", 20, "seconds before sending the emergency trigger")
	evictOnly := flag.Bool("evict", false, "drop the file from the page cache (no root needed) and exit")
	flag.Parse()

	size := int64(*sizeGB) << 30

	if *create {
		createFile(*path, size)
		return
	}
	if *evictOnly {
		if err := evict(*path); err != nil {
			log.Fatalf("evict %s: %v", *path, err)
		}
		return
	}

	f, err := os.Open(*path) // a plain open: reads go through the page cache
	if err != nil {
		log.Fatalf("open %s: %v (did you run with -create first?)", *path, err)
	}
	defer f.Close()

	var ops atomic.Int64 // reads completed in the current second
	stop := make(chan struct{})
	var wg sync.WaitGroup

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, pageSize)
			pages := size / pageSize
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Pick a random page and read it. Random access defeats the
				// kernel's read-ahead, so performance depends on the cache.
				off := rand.Int64N(pages) * pageSize
				if _, err := f.ReadAt(buf, off); err != nil {
					log.Fatalf("read: %v", err)
				}
				ops.Add(1)
			}
		}()
	}

	// Principle 2 demo: tell the controller we have entered an emergency.
	if *triggerFile != "" {
		go func() {
			time.Sleep(time.Duration(*triggerAfter) * time.Second)
			if err := os.WriteFile(*triggerFile, []byte("emergency\n"), 0644); err != nil {
				log.Printf("could not write trigger: %v", err)
				return
			}
			fmt.Fprintf(os.Stderr, "cacheapp: sent EMERGENCY trigger at t=%ds\n", *triggerAfter)
		}()
	}

	fmt.Println("t,ops_per_sec")
	var total, counted int64
	tick := time.NewTicker(time.Second)
	for t := 1; t <= *duration; t++ {
		<-tick.C
		n := ops.Swap(0) // read and reset the per-second counter
		fmt.Printf("%d,%d\n", t, n)
		if t > *warmup {
			total += n
			counted++
		}
	}
	close(stop)
	wg.Wait()

	if counted > 0 {
		fmt.Printf("AVG_OPS_PER_SEC %d\n", total/counted)
	}
}

// createFile writes real (non-zero) data. A sparse file made with Truncate
// would be read back as zeros without touching the disk, which would hide
// the very cost we want to measure.
func createFile(path string, size int64) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	chunk := make([]byte, 1<<20) // 1 MB
	for i := range chunk {
		chunk[i] = byte(rand.IntN(256))
	}
	for written := int64(0); written < size; written += int64(len(chunk)) {
		if _, err := f.Write(chunk); err != nil {
			log.Fatal(err)
		}
	}
	if err := f.Sync(); err != nil { // make sure it is really on disk
		log.Fatal(err)
	}
	fmt.Printf("created %s (%d GB)\n", path, size>>30)
}
