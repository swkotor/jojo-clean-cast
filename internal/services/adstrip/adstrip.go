// Package adstrip removes dynamically-inserted ads from podcast MP3s by
// comparing two or more downloads of the SAME episode.
//
// Why this works. Hosts like Acast, Megaphone and Art19 do server-side ad
// stitching: the show itself is encoded once, and at download time the CDN
// concatenates pre-encoded ad segments around it ("livestitches" in Acast's
// URLs). Two listeners therefore receive files whose show frames are byte for
// byte identical and whose ad frames differ. Intersecting the streams leaves
// the show.
//
// Measured on a real VGC episode: two fetches shared 99.0% of their MP3 frames
// and differed in exactly two places - a 37s pre-roll present in one fetch only,
// and a mid-roll slot holding 20.1s in one and 15.1s in the other.
//
// This is frame-accurate and needs no transcription, no heuristics and no
// guessing about content. Its limits are equally clear:
//
//   - An ad slot that happens to receive the IDENTICAL ad in every fetch is
//     invisible to the method. More variants make that less likely.
//   - Host-read sponsorships baked into the original recording are part of the
//     show's own encode, so they are NOT removed. Only dynamic insertions are.
//   - It costs one extra download per variant.
//
// Cuts land on frame boundaries, which is where MP3 can be cut cleanly; the
// output is a plain MP3 with the same codec parameters as the input.
package adstrip

import (
	"errors"
	"fmt"
	"hash/maphash"
	"os"
	"sort"
)

// MPEG-1 Layer III bitrates (kbit/s), indexed by the header's bitrate field.
var bitrates = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}

// Sampling rates, indexed by the header's sampling-rate field.
var sampleRates = [4]int{44100, 48000, 32000, 0}

// Frame is one MPEG audio frame: the raw bytes exactly as they must be written
// back out, plus how long it plays for.
type Frame struct {
	Data    []byte
	Seconds float64
}

// Parse walks an MPEG-1 Layer III file and returns its audio frames.
//
// It is deliberately forgiving: a stitched file is a concatenation of
// separately-encoded pieces, so it can carry ID3 tags, Xing/LAME headers and
// stray bytes at the seams. Anything that isn't a valid frame header is
// skipped a byte at a time rather than treated as an error.
func Parse(path string) ([]Frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseBytes(data), nil
}

// ParseBytes is Parse over an in-memory file.
func ParseBytes(d []byte) []Frame {
	i := 0
	// Skip an ID3v2 container if present (syncsafe size, 7 bits per byte).
	if len(d) > 10 && d[0] == 'I' && d[1] == 'D' && d[2] == '3' {
		size := int(d[6])<<21 | int(d[7])<<14 | int(d[8])<<7 | int(d[9])
		if 10+size < len(d) {
			i = 10 + size
		}
	}
	frames := make([]Frame, 0, len(d)/417+1)
	for i+4 <= len(d) {
		if d[i] != 0xFF || d[i+1]&0xE0 != 0xE0 {
			i++
			continue
		}
		version := (d[i+1] >> 3) & 3 // 3 == MPEG-1
		layer := (d[i+1] >> 1) & 3   // 1 == Layer III
		brIdx := (d[i+2] >> 4) & 0xF
		srIdx := (d[i+2] >> 2) & 3
		padding := int((d[i+2] >> 1) & 1)
		if version != 3 || layer != 1 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
			i++
			continue
		}
		sr := sampleRates[srIdx]
		length := (144*bitrates[brIdx]*1000)/sr + padding
		if length < 24 || i+length > len(d) {
			i++
			continue
		}
		frames = append(frames, Frame{
			Data:    d[i : i+length],
			Seconds: 1152.0 / float64(sr),
		})
		i += length
	}
	return frames
}

// Duration totals a frame slice.
func Duration(frames []Frame) float64 {
	var t float64
	for _, f := range frames {
		t += f.Seconds
	}
	return t
}

var hashSeed = maphash.MakeSeed()

func hashFrames(frames []Frame) []uint64 {
	out := make([]uint64, len(frames))
	for i, f := range frames {
		var h maphash.Hash
		h.SetSeed(hashSeed)
		h.Write(f.Data)
		out[i] = h.Sum64()
	}
	return out
}

// run is a maximal stretch of frames that matched between two variants.
type run struct{ aStart, bStart, length int }

// commonRuns aligns two frame-hash sequences and returns the stretches they
// share, in order.
//
// This is anchor-based (patience-diff style) rather than a full LCS: compressed
// audio frames are essentially unique, so hashes appearing exactly once in both
// sequences are near-certain true pairings. Anchoring on those and taking the
// longest increasing run gives the alignment in O(n log n) - a full LCS over
// ~75,000 frames per file would be far too slow.
func commonRuns(a, b []uint64) []run {
	countA := make(map[uint64]int, len(a))
	for _, h := range a {
		countA[h]++
	}
	indexB := make(map[uint64]int, len(b))
	countB := make(map[uint64]int, len(b))
	for i, h := range b {
		countB[h]++
		indexB[h] = i
	}

	type anchor struct{ ai, bi int }
	anchors := make([]anchor, 0, len(a))
	for ai, h := range a {
		if countA[h] == 1 && countB[h] == 1 {
			anchors = append(anchors, anchor{ai, indexB[h]})
		}
	}
	if len(anchors) == 0 {
		return nil
	}
	// anchors are already ascending in ai; keep the longest run that is also
	// ascending in bi, so the alignment never crosses itself.
	tailsIdx := make([]int, 0, len(anchors))
	prev := make([]int, len(anchors))
	tailsB := make([]int, 0, len(anchors))
	for i, an := range anchors {
		pos := sort.SearchInts(tailsB, an.bi)
		if pos == len(tailsB) {
			tailsB = append(tailsB, an.bi)
			tailsIdx = append(tailsIdx, i)
		} else {
			tailsB[pos] = an.bi
			tailsIdx[pos] = i
		}
		if pos > 0 {
			prev[i] = tailsIdx[pos-1]
		} else {
			prev[i] = -1
		}
	}
	chain := make([]anchor, 0, len(tailsIdx))
	for i := tailsIdx[len(tailsIdx)-1]; i >= 0; i = prev[i] {
		chain = append(chain, anchors[i])
		if prev[i] == -1 {
			break
		}
	}
	for l, r := 0, len(chain)-1; l < r; l, r = l+1, r-1 {
		chain[l], chain[r] = chain[r], chain[l]
	}

	// Grow each anchor into the largest identical stretch around it, merging
	// with the previous run when they meet.
	runs := make([]run, 0, len(chain))
	for _, an := range chain {
		ai, bi := an.ai, an.bi
		if n := len(runs); n > 0 {
			last := runs[n-1]
			if ai < last.aStart+last.length || bi < last.bStart+last.length {
				continue
			}
		}
		start, startB := ai, bi
		for start > 0 && startB > 0 && a[start-1] == b[startB-1] {
			if n := len(runs); n > 0 && (start-1 < runs[n-1].aStart+runs[n-1].length) {
				break
			}
			start--
			startB--
		}
		end, endB := ai+1, bi+1
		for end < len(a) && endB < len(b) && a[end] == b[endB] {
			end++
			endB++
		}
		if n := len(runs); n > 0 && runs[n-1].aStart+runs[n-1].length == start &&
			runs[n-1].bStart+runs[n-1].length == startB {
			runs[n-1].length += end - start
			continue
		}
		runs = append(runs, run{start, startB, end - start})
	}
	return runs
}

// Gap is a stretch of the base file that was NOT present in every variant -
// i.e. a dynamically inserted ad.
type Gap struct {
	At      float64 // seconds into the base file
	Seconds float64
}

// Result reports what an intersection removed.
type Result struct {
	Frames    []Frame // the show, ads removed
	Original  float64 // seconds before
	Clean     float64 // seconds after
	Removed   float64 // seconds cut
	Gaps      []Gap   // where the cuts were
	Variants  int
	SharedPct float64 // how much of the base survived; a sanity signal
}

// Intersect keeps only the frames present in every variant, in base order.
//
// variants[0] is the base: the output is its frames minus anything missing from
// any other variant. Two variants is the practical minimum; more make it less
// likely that a slot filled with the same ad twice slips through.
func Intersect(variants [][]Frame) (*Result, error) {
	if len(variants) < 2 {
		return nil, errors.New("adstrip: need at least two downloads to compare")
	}
	base := variants[0]
	if len(base) == 0 {
		return nil, errors.New("adstrip: base download has no audio frames")
	}
	baseHash := hashFrames(base)

	keep := make([]int, len(base))
	for i := range keep {
		keep[i] = i
	}
	for _, other := range variants[1:] {
		if len(other) == 0 {
			return nil, errors.New("adstrip: a comparison download has no audio frames")
		}
		cur := make([]uint64, len(keep))
		for i, idx := range keep {
			cur[i] = baseHash[idx]
		}
		runs := commonRuns(cur, hashFrames(other))
		next := make([]int, 0, len(keep))
		for _, r := range runs {
			for k := r.aStart; k < r.aStart+r.length; k++ {
				next = append(next, keep[k])
			}
		}
		keep = next
		if len(keep) == 0 {
			return nil, errors.New("adstrip: downloads share no audio at all - not the same episode?")
		}
	}

	res := &Result{Variants: len(variants), Original: Duration(base)}
	res.Frames = make([]Frame, 0, len(keep))
	kept := make(map[int]bool, len(keep))
	for _, idx := range keep {
		kept[idx] = true
		res.Frames = append(res.Frames, base[idx])
	}
	res.Clean = Duration(res.Frames)
	res.Removed = res.Original - res.Clean
	res.SharedPct = 100 * float64(len(keep)) / float64(len(base))

	var at float64
	var gapStart, gapLen float64
	inGap := false
	for i, f := range base {
		if kept[i] {
			if inGap {
				res.Gaps = append(res.Gaps, Gap{At: gapStart, Seconds: gapLen})
				inGap = false
			}
		} else {
			if !inGap {
				gapStart, gapLen, inGap = at, 0, true
			}
			gapLen += f.Seconds
		}
		at += f.Seconds
	}
	if inGap {
		res.Gaps = append(res.Gaps, Gap{At: gapStart, Seconds: gapLen})
	}
	return res, nil
}

// Write concatenates frames into an MP3.
//
// No Xing/LAME header is written: the stitched sources are constant-bitrate, so
// players derive the duration from the bitrate correctly, and a stale Xing
// frame copied from the input would claim the ORIGINAL length and make players
// show a duration that includes the removed ads.
func Write(path string, frames []Frame) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, fr := range frames {
		if _, err := f.Write(fr.Data); err != nil {
			return err
		}
	}
	return f.Sync()
}

// String renders a Result for the event log.
func (r *Result) String() string {
	s := fmt.Sprintf("%.0fs -> %.0fs (cut %.0fs across %d slot(s), %d variants, %.1f%% shared)",
		r.Original, r.Clean, r.Removed, len(r.Gaps), r.Variants, r.SharedPct)
	for _, g := range r.Gaps {
		s += fmt.Sprintf("; %.0fs@%.0fs", g.Seconds, g.At)
	}
	return s
}
