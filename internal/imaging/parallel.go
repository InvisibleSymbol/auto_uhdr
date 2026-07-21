package imaging

import (
	"runtime"
	"sync"
)

// ParallelRows splits [0,h) into contiguous bands and runs fn(y0,y1) concurrently, one band per
// worker. Used to parallelize per-row image loops across cores.
func ParallelRows(h int, fn func(y0, y1 int)) {
	workers := runtime.NumCPU()
	if workers > h {
		workers = h
	}
	if workers <= 1 {
		fn(0, h)
		return
	}
	var wg sync.WaitGroup
	band := (h + workers - 1) / workers
	for y0 := 0; y0 < h; y0 += band {
		y1 := y0 + band
		if y1 > h {
			y1 = h
		}
		wg.Add(1)
		go func(a, b int) {
			defer wg.Done()
			fn(a, b)
		}(y0, y1)
	}
	wg.Wait()
}
