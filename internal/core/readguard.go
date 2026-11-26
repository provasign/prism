package core

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/provasign/prism/internal/sensitive"
)

// readWholeLimit is the size above which a file is not loaded whole: the
// limit rg already applies to search (SEC-04).
const readWholeLimit = 2 << 20

// guardedFile is a file as prism_read may deliver it.
type guardedFile struct {
	data []byte
	// refused, when set, replaces the content: not a regular file, binary,
	// or a sensitive file the caller did not ask for.
	refused string
	// note qualifies data (a large file read in part).
	note string
	size int64
	// totalLines is the file's line count when a large file was read in
	// part (the delivered data stops at the range), else 0.
	totalLines int
}

// readGuarded reads abs for prism_read (SEC-03, SEC-04). from/to (1-based,
// inclusive, 0 = whole file) let a large file be read without loading it:
// the lines before `from` come back empty, so callers slice by line number
// as before.
func readGuarded(abs, rel string, includeSensitive bool, from, to int) (guardedFile, error) {
	fi, err := os.Stat(abs)
	if err != nil {
		return guardedFile{}, err
	}
	g := guardedFile{size: fi.Size()}
	if !fi.Mode().IsRegular() {
		// A FIFO blocked forever; a device or socket is not source.
		g.refused = "not a regular file"
		return g, nil
	}
	if !includeSensitive && sensitive.Match(rel) {
		g.refused = "sensitive file: " + sensitive.Withheld
		return g, nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return guardedFile{}, err
	}
	defer f.Close()
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		g.refused = fmt.Sprintf("binary file, %d bytes", fi.Size())
		return g, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return guardedFile{}, err
	}
	if fi.Size() <= readWholeLimit {
		g.data, err = io.ReadAll(f)
		return g, err
	}
	if from <= 0 {
		buf := make([]byte, readWholeLimit)
		n, _ := io.ReadFull(f, buf)
		buf = buf[:n]
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			buf = buf[:i+1]
		}
		g.data = buf
		g.note = fmt.Sprintf("file is %d bytes; delivered the first %d lines (%d bytes) — read a later range with from/to", fi.Size(), bytes.Count(buf, []byte("\n")), len(buf))
		return g, nil
	}
	if to < from {
		to = from + 240
	}
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	line := 0
	for sc.Scan() {
		line++
		if line > to {
			continue // count the rest; do not keep it
		}
		if line >= from {
			b.WriteString(sc.Text())
		}
		b.WriteByte('\n')
	}
	g.totalLines = line
	g.data = []byte(b.String())
	g.note = fmt.Sprintf("file is %d bytes; read lines %d-%d without loading the whole file", fi.Size(), from, to)
	return g, sc.Err()
}

// refusedReadResult is prism_read's answer for a file it does not deliver.
func refusedReadResult(file string, g guardedFile) map[string]any {
	return map[string]any{"file": file, "found": true, "delivered": false, "size": g.size, "warning": g.refused}
}
