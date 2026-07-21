package imaging

import (
	"runtime"
	"sync"
)

// ParallelBands splits [0,n) into up to GOMAXPROCS contiguous bands and runs
// fn(lo,hi) for each concurrently, blocking until all bands finish. It is the
// single work-splitting primitive the image loops build on (rows, columns, or
// flat pixel ranges — the caller decides what n means).
func ParallelBands(n int, fn func(lo, hi int)) {
	if n <= 0 {
		return
	}
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers <= 1 {
		fn(0, n)
		return
	}
	band := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += band {
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			fn(lo, hi)
		}(lo, min(lo+band, n))
	}
	wg.Wait()
}

// ParallelRows runs fn over horizontal bands of an h-row image. It is a named
// alias for [ParallelBands] that reads naturally at per-row loops.
func ParallelRows(h int, fn func(y0, y1 int)) { ParallelBands(h, fn) }
