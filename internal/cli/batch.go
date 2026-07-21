package cli

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/invis/arw2uhdr"
)

// RunBatch implements `arw2uhdr batch`: convert every paired ARW found under the
// given directories/files, with a bounded worker pool. Failures are logged, not
// fatal; a summary is printed and a non-zero code returned if any pair failed.
func RunBatch(args []string) error {
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf convertFlags
	cf.register(fs)
	jobs := fs.Int("j", 2, "parallel jobs (each needs ~1.5 GB RAM at 20 MP)")
	outDir := fs.String("o", "", "output directory (default: next to each JPEG)")
	skip := fs.Bool("skip-existing", false, "skip pairs whose output already exists")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: arw2uhdr batch [flags] <dir|file>...\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &ExitError{Code: ExitUsage}
	}
	roots := fs.Args()
	if len(roots) == 0 {
		return usageErr("batch: missing <dir|file>...")
	}
	opts, err := cf.options()
	if err != nil {
		return err
	}

	pairs, err := discoverPairs(roots, *outDir)
	if err != nil {
		return inputErr("%v", err)
	}
	if len(pairs) == 0 {
		fmt.Println("batch: no ARW+JPEG pairs found")
		return nil
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			return &ExitError{Code: ExitWrite, Message: err.Error()}
		}
	}
	fmt.Printf("batch: %d pair(s), %d job(s)\n", len(pairs), *jobs)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	conv := arw2uhdr.New(opts)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed atomic.Int64
		sem    = make(chan struct{}, max(*jobs, 1))
	)
	for _, in := range pairs {
		if ctx.Err() != nil {
			break
		}
		if *skip {
			if _, err := os.Stat(in.Output); err == nil {
				fmt.Printf("SKIP  %s\n", filepath.Base(in.Output))
				continue
			}
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(in arw2uhdr.Input) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := conv.Convert(ctx, in)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed.Add(1)
				fmt.Fprintf(os.Stderr, "FAIL  %s: %v\n", filepath.Base(in.ARW), err)
				return
			}
			fmt.Printf("OK    %s -> %s (%.1fs)\n", filepath.Base(in.ARW), filepath.Base(res.Output), float64(res.ElapsedMs)/1000)
		}(in)
	}
	wg.Wait()

	nFail := failed.Load()
	fmt.Printf("batch: done (%d ok, %d failed)\n", int64(len(pairs))-nFail, nFail)
	if nFail > 0 {
		return &ExitError{Code: ExitEncode, Message: ""}
	}
	return nil
}

// discoverPairs walks roots for *.arw files that have a sibling JPEG, returning
// sorted, de-duplicated Input records.
func discoverPairs(roots []string, outDir string) ([]arw2uhdr.Input, error) {
	seen := map[string]bool{}
	var pairs []arw2uhdr.Input
	add := func(arw string) {
		if seen[arw] {
			return
		}
		jpg := inferJPEG(arw)
		if jpg == "" {
			return
		}
		seen[arw] = true
		out := deriveOutput(jpg)
		if outDir != "" {
			out = filepath.Join(outDir, filepath.Base(out))
		}
		pairs = append(pairs, arw2uhdr.Input{ARW: arw, JPEG: jpg, Output: out})
	}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("cannot access %s: %w", root, err)
		}
		if !info.IsDir() {
			if isARW(root) {
				add(root)
			}
			continue
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if !d.IsDir() && isARW(path) {
				add(path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(pairs, func(a, b arw2uhdr.Input) int { return strings.Compare(a.ARW, b.ARW) })
	return pairs, nil
}

func isARW(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".arw")
}
