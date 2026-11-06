package buslog

// Tamper evidence: a per-record hash chain.
//
// Every record carries "seq" (1, 2, 3, ... across the whole log, over segment
// boundaries) and "prev": the SHA-256 of the previous record's exact line
// bytes (without the newline), hex encoded. The first record's prev is 64
// zeros. Because a record's own line includes its prev, the hash of record N
// commits to every record before it AND to N's own prev. The hash is over the
// raw written bytes, not over parsed fields, so there is no canonicalisation
// to get wrong.
//
// A chain alone proves nothing about the END of the log: dropping the last
// records leaves a valid shorter chain. So the daemon also keeps <dir>/chain.head,
// a fixed-width "seq hash" line naming the newest record, rewritten after every
// append. Verify requires the log to reach it and to match it.
//
// What this does not do: the hash is unkeyed. Someone who can write the log
// directory can rewrite records AND recompute the chain AND chain.head. The
// defence against that is to record the tip somewhere they cannot reach
// (Verify prints it; --tip checks it). Within that limit every edit that
// does not recompute the chain is detected.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// genesis is the prev of the first record.
var genesis = strings.Repeat("0", 64)

// HeadFile is the anchor file name inside a log directory.
const HeadFile = "chain.head"

func hashLine(line []byte) string {
	h := sha256.Sum256(line)
	return hex.EncodeToString(h[:])
}

func headLine(seq uint64, hash string) string { return fmt.Sprintf("%020d %s\n", seq, hash) }

func (l *Log) writeHead() error {
	if l.head == nil {
		f, err := os.OpenFile(filepath.Join(l.dir, HeadFile), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		l.head = f
	}
	_, err := l.head.WriteAt([]byte(headLine(l.tipSeq, l.tipHash)), 0) // fixed width: in place
	return err
}

// recoverTip finds the newest record so appends continue the chain. A final
// line without its newline is a write that never completed: it is cut off
// (nothing acknowledged it) so the next record does not fuse with it.
func (l *Log) recoverTip(paths []string) error {
	for i := len(paths) - 1; i >= 0; i-- {
		b, err := os.ReadFile(paths[i])
		if err != nil {
			return err
		}
		if i == len(paths)-1 && len(b) > 0 && b[len(b)-1] != '\n' {
			cut := bytes.LastIndexByte(b, '\n') + 1
			if err := os.Truncate(paths[i], int64(cut)); err != nil {
				return err
			}
			b = b[:cut]
		}
		if len(b) == 0 {
			continue
		}
		b = b[:len(b)-1]
		line := b[bytes.LastIndexByte(b, '\n')+1:]
		var r struct{ Seq uint64 }
		if json.Unmarshal(line, &r) != nil {
			return fmt.Errorf("%s: last record is not JSON; refusing to extend the chain", paths[i])
		}
		l.tipSeq, l.tipHash = r.Seq, hashLine(line) // Seq 0: legacy tail, a fresh chain starts
		if r.Seq == 0 {
			l.tipHash = genesis
		}
		return nil
	}
	return nil
}

// ChainStatus is the verdict of Verify.
type ChainStatus int

const (
	// ChainBroken: the log was altered (or is damaged). Problems says where.
	ChainBroken ChainStatus = iota
	// ChainVerified: every record chains to the one before, from the first to
	// the anchored tip.
	ChainVerified
	// ChainUnchained: records carry no chain (written before it existed). They
	// cannot be verified; this is not a pass.
	ChainUnchained
)

// ChainReport is what Verify found.
type ChainReport struct {
	Status   ChainStatus
	Records  int
	Chained  int
	Legacy   int    // records without a chain before the chain starts
	TipSeq   uint64 // newest chained record
	TipHash  string // its line hash: record it somewhere safe
	Anchored bool   // chain.head was present and checked
	Problems []string
	Notes    []string
}

// Verify checks the chain of a log directory (or a single segment file: then
// there is no anchor, and the caller is told so). wantTip, if non-empty, must
// equal the hash of the newest record.
func Verify(path, wantTip string) (ChainReport, error) {
	var r ChainReport
	st, err := os.Stat(path)
	if err != nil {
		return r, err
	}
	paths, dir := []string{path}, ""
	if st.IsDir() {
		dir = path
		if paths, err = segmentPaths(path); err != nil {
			return r, err
		}
		if len(paths) == 0 {
			return r, fmt.Errorf("no seg-*.jsonl files in %s", path)
		}
	}
	bad := func(f string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(f, a...)) }

	prevHash, lastSeq := genesis, uint64(0)
	hashAt := map[uint64]string{}
	for pi, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return r, err
		}
		name := filepath.Base(p)
		if pi == len(paths)-1 && len(b) > 0 && b[len(b)-1] != '\n' {
			bad("%s: ends in a partial record (%d bytes without newline): cut off mid-write or truncated",
				name, len(b)-bytes.LastIndexByte(b, '\n')-1)
			b = b[:bytes.LastIndexByte(b, '\n')+1]
		}
		for n, line := range bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")) {
			if len(b) == 0 {
				break
			}
			r.Records++
			where := fmt.Sprintf("%s line %d", name, n+1)
			var rec struct {
				Seq  uint64
				Prev string
			}
			if err := json.Unmarshal(line, &rec); err != nil {
				bad("%s: not a JSON record", where)
				prevHash = hashLine(line)
				continue
			}
			if rec.Seq == 0 && rec.Prev == "" {
				if r.Chained > 0 {
					bad("%s: record without a chain after chained records (spliced in)", where)
				} else {
					r.Legacy++
				}
				continue
			}
			r.Chained++
			switch {
			case rec.Seq != lastSeq+1 && rec.Prev != prevHash:
				bad("%s: seq %d after %d and prev does not match the previous record", where, rec.Seq, lastSeq)
			case rec.Seq != lastSeq+1:
				bad("%s: seq %d, expected %d", where, rec.Seq, lastSeq+1)
			case rec.Prev != prevHash:
				bad("%s: prev does not match the hash of the previous record (seq %d)", where, rec.Seq)
			}
			lastSeq, prevHash = rec.Seq, hashLine(line)
			hashAt[lastSeq] = prevHash
		}
	}
	r.TipSeq = lastSeq
	if r.Chained > 0 {
		r.TipHash = prevHash
	}

	switch {
	case dir == "":
		r.Notes = append(r.Notes, "single file: no chain.head, so a cut-off tail cannot be detected")
	case r.Chained == 0 && r.Legacy > 0:
		// legacy directory: no anchor expected
	default:
		hb, err := os.ReadFile(filepath.Join(dir, HeadFile))
		var hseq uint64
		var hhash string
		if err == nil {
			f := strings.Fields(string(hb))
			if len(f) == 2 {
				hseq, _ = strconv.ParseUint(f[0], 10, 64)
				hhash = f[1]
			}
		}
		switch {
		case err != nil || hhash == "":
			bad("%s missing or unreadable: the end of the log is unanchored (cannot rule out truncation)", HeadFile)
		case hseq > lastSeq:
			bad("log ends at seq %d but %s says seq %d: %d record(s) missing from the end", lastSeq, HeadFile, hseq, hseq-lastSeq)
		case hashAt[hseq] != hhash:
			bad("record seq %d does not match %s (hash %.12s..., anchor %.12s...)", hseq, HeadFile, hashAt[hseq], hhash)
		default:
			r.Anchored = true
			if hseq < lastSeq {
				r.Notes = append(r.Notes, fmt.Sprintf("%d record(s) beyond %s (crash between log write and head write, or appended later)", lastSeq-hseq, HeadFile))
			}
		}
	}
	if wantTip != "" && r.TipHash != wantTip {
		bad("tip hash %.16s... is not the expected %.16s...", r.TipHash, wantTip)
	}
	switch {
	case len(r.Problems) > 0:
		r.Status = ChainBroken
	case r.Chained == 0:
		r.Status = ChainUnchained
	case r.Legacy > 0:
		r.Status = ChainUnchained
		r.Notes = append(r.Notes, fmt.Sprintf("%d leading record(s) have no chain and cannot be verified", r.Legacy))
	default:
		r.Status = ChainVerified
	}
	return r, nil
}
