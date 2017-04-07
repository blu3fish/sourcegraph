package search

import (
	"archive/zip"
	"bufio"
	"context"
	"io"
	"regexp"
	"strings"
	"sync"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
)

const (
	// maxFileSize is the limit on file size in bytes. Only files smaller
	// than this are searched.
	maxFileSize = 1 << 19 // 512KB

	// maxFileMatches is the limit on number of matching files we return.
	maxFileMatches = 1000

	// maxLineMatches is the limit on number of matches to return in a
	// file.
	maxLineMatches = 100

	// maxOffsets is the limit on number of matches to return on a line.
	maxOffsets = 10

	// numWorkers is how many concurrent readerGreps run per
	// concurrentFind
	numWorkers = 8
)

// readerGrep is responsible for finding LineMatches. It is not concurrency
// safe (it reuses buffers for performance).
//
// This code is base on reading the techniques detailed in
// http://blog.burntsushi.net/ripgrep/
//
// The stdlib regexp is pretty powerful and in fact implements many of the
// features in ripgrep. Our implementation gives high performance via pruning
// aggressively which files to consider (non-binary under a limit) and
// optimizing for assuming most lines will not contain a match. The pruning of
// files is done by the store.
//
// If there is no more low-hanging fruit and perf is not acceptable, we could
// consider an using ripgrep directly (modify it to search zip archives).
//
// TODO(keegan) search for candidate lines without parsing lines. (regexp.LiteralPrefix + optimize default ignore case)
// TODO(keegan) return search statistics
type readerGrep struct {
	// re is the regexp to match.
	re *regexp.Regexp

	// ignoreCase if true means we need to do case insensitive matching.
	ignoreCase bool

	// reader is reused between file searches to avoid re-allocating the
	// underlying buffer.
	reader *bufio.Reader

	// transformBuf is reused between file searches to avoid
	// re-allocating. It is only used if we need to transform the input
	// before matching. For example we lower case the input in the case of
	// ignoreCase.
	transformBuf []byte
}

// compile returns a readerGrep for matching p.
func compile(p *Params) (*readerGrep, error) {
	var (
		expr       = p.Pattern
		ignoreCase bool
	)
	if !p.IsRegExp {
		expr = regexp.QuoteMeta(expr)
	}
	if p.IsWordMatch {
		expr = `\b` + expr + `\b`
	}
	if !p.IsCaseSensitive {
		// We don't just use (?i) because regexp library doesn't seem
		// to contain good optimizations for case insensitive
		// search. Instead we lowercase the input and pattern.
		// TODO(keegan) Lowercasing the pattern naively with just
		// strings.ToLower mangles valid regex's tokens such as \S
		expr = strings.ToLower(expr)
		ignoreCase = true
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	return &readerGrep{
		re:         re,
		ignoreCase: ignoreCase,
	}, nil
}

// Copy returns a copied version of rg that is safe to use from another
// goroutine.
func (rg *readerGrep) Copy() *readerGrep {
	return &readerGrep{
		re:         rg.re.Copy(),
		ignoreCase: rg.ignoreCase,
	}
}

// Find returns a LineMatch for each line that matches rg in reader.
//
// NOTE: This is not safe to use concurrently.
func (rg *readerGrep) Find(reader io.Reader) ([]LineMatch, error) {
	r := rg.reader
	if r == nil {
		r = bufio.NewReader(reader)
		rg.reader = r
		if rg.ignoreCase {
			rg.transformBuf = make([]byte, 0, 4096) // 4096 is the size of bufio.NewReader
		}
	} else {
		r.Reset(reader)
	}

	var matches []LineMatch
	for i := 0; len(matches) < maxLineMatches; i++ {
		lineBuf, isPrefix, err := r.ReadLine()
		if isPrefix || err != nil {
			// We have either found a long line, encountered an
			// error or reached EOF. We skip files with long lines
			// since the user is unlikely interested in the
			// result, so the only case we want to return matches
			// is if we have reached the end of file.
			if err == io.EOF {
				break
			}
			return nil, err
		}
		// matchBuf is what we run match on, lineBuf is the original
		// line (for Preview).
		matchBuf := lineBuf

		// If we are ignoring case, we transform the input instead of
		// relying on the regular expression engine which can be
		// slow. compile has already lowercased the pattern. We also
		// trade some correctness for perf by using a non-utf8 aware
		// lowercase function.
		if rg.ignoreCase {
			matchBuf = rg.transformBuf[:len(lineBuf)]
			bytesToLowerASCII(matchBuf, lineBuf)
		}

		// FindAllIndex allocates memory. We can avoid that by just
		// checking if we have a match first. We expect most lines to
		// not have a match, so we trade a bit of repeated computation
		// to avoid unnecessary allocations.
		if rg.re.Find(matchBuf) != nil {
			locs := rg.re.FindAllIndex(matchBuf, maxOffsets)
			offsetAndLengths := make([][]int, len(locs))
			for i, match := range locs {
				start, end := match[0], match[1]
				offsetAndLengths[i] = []int{start, end - start}
			}
			matches = append(matches, LineMatch{
				// making a copy of lineBuf is intentional, the underlying array of b can be overwritten by scanner.
				Preview:          string(lineBuf),
				LineNumber:       i,
				OffsetAndLengths: offsetAndLengths,
			})
		}
	}
	return matches, nil
}

// FindZip is a convenience function to run Find on f.
func (rg *readerGrep) FindZip(f *zip.File) (FileMatch, error) {
	rc, err := f.Open()
	if err != nil {
		return FileMatch{}, err
	}
	lm, err := rg.Find(rc)
	rc.Close()
	return FileMatch{
		Path:        f.Name,
		LineMatches: lm,
	}, err
}

// concurrentFind searches files in zr looking for matches using rg.
func concurrentFind(ctx context.Context, rg *readerGrep, zr *zip.Reader) (fm []FileMatch, err error) {
	span, ctx := opentracing.StartSpanFromContext(ctx, "ConcurrentFind")
	ext.Component.Set(span, "matcher")
	defer func() {
		if err != nil {
			ext.Error.Set(span, true)
			span.SetTag("err", err.Error())
		}
		span.Finish()
	}()

	// If we reach maxFileMatches we use cancel to stop the search
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		files     = make(chan *zip.File)
		matches   = make(chan FileMatch)
		wg        sync.WaitGroup
		wgErrOnce sync.Once
		wgErr     error
	)

	// goroutine responsible for writing to files. It also is the only
	// goroutine which listens for cancellation.
	go func() {
		done := ctx.Done()
		for _, f := range zr.File {
			select {
			case files <- f:
			case <-done:
				close(files)
				return
			}
		}
		close(files)
	}()

	// Start workers. They read from files and write to matches.
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(rg *readerGrep) {
			defer wg.Done()
			for f := range files {
				fm, err := rg.FindZip(f)
				if err != nil {
					wgErrOnce.Do(func() {
						wgErr = err
						// Drain files
						for range files {
						}
					})
					return
				}
				if len(fm.LineMatches) > 0 {
					matches <- fm
				}
			}
		}(rg.Copy())
	}

	// Wait for workers to be done. Signal to collector there is no more
	// results coming by closing matches.
	go func() {
		wg.Wait()
		close(matches)
	}()

	// Collect all matches. Do not return a nil slice if we find nothing
	// so we can nicely serialize it.
	m := []FileMatch{}
	for fm := range matches {
		m = append(m, fm)
		if len(m) >= maxFileMatches {
			cancel()
			// drain matches
			for range matches {
			}
		}
	}
	return m, wgErr
}

// python to generate ', '.join(hex(ord(chr(i).lower())) for i in range(256))
var lowerTable = [256]uint8{
	0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0xa, 0xb, 0xc, 0xd, 0xe, 0xf,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
	0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f,
	0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x3f,
	0x40, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f,
	0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x5b, 0x5c, 0x5d, 0x5e, 0x5f,
	0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f,
	0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x7b, 0x7c, 0x7d, 0x7e, 0x7f,
	0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89, 0x8a, 0x8b, 0x8c, 0x8d, 0x8e, 0x8f,
	0x90, 0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0x99, 0x9a, 0x9b, 0x9c, 0x9d, 0x9e, 0x9f,
	0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf,
	0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf,
	0xc0, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xcb, 0xcc, 0xcd, 0xce, 0xcf,
	0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda, 0xdb, 0xdc, 0xdd, 0xde, 0xdf,
	0xe0, 0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea, 0xeb, 0xec, 0xed, 0xee, 0xef,
	0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xf9, 0xfa, 0xfb, 0xfc, 0xfd, 0xfe, 0xff,
}

func bytesToLowerASCII(dst, src []byte) {
	// we assume len(dst) >= len(src). We do the below to hint to the
	// compiler to eliminate bounds check on dst.
	dst = dst[:len(src)]
	for i := range src {
		dst[i] = lowerTable[src[i]]
	}
}
