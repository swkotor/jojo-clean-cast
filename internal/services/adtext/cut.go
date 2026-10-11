package adtext

// Cutting spans out of audio that is not MPEG Layer III.
//
// For MP3 we drop whole frames ourselves, which is exact and needs no external
// tool. That does not generalise: the YouTube-sourced episodes are AAC in an
// MP4 container, where the audio samples are indexed by container tables, so
// deleting bytes is not an option — the tables have to be rebuilt.
//
// ffmpeg does exactly that with a stream copy: each kept stretch is demuxed and
// remuxed without re-encoding, then the pieces are concatenated. The audio is
// never decoded, so there is no generation loss. AAC frames are 1024 samples
// (~23ms at 44.1kHz) and every one is independently decodable, so cuts land
// within one frame of where we asked.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ProbeDuration returns the playing time of an audio file in seconds.
func ProbeDuration(path string) (float64, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0, fmt.Errorf("probing duration: %v", err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("could not read a duration from %s", filepath.Base(path))
	}
	return d, nil
}

// keepRanges inverts the ad spans into the stretches worth keeping.
func keepRanges(spans []Span, total float64) [][2]float64 {
	var out [][2]float64
	at := 0.0
	for _, sp := range spans {
		if sp.Start > at {
			out = append(out, [2]float64{at, min(sp.Start, total)})
		}
		if sp.End > at {
			at = sp.End
		}
	}
	if at < total {
		out = append(out, [2]float64{at, total})
	}
	// Drop slivers: a fragment shorter than a second is not listenable content
	// and only adds a seam.
	kept := out[:0]
	for _, r := range out {
		if r[1]-r[0] >= 1 {
			kept = append(kept, r)
		}
	}
	return kept
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// CutStreamCopy rewrites path with the given spans removed, re-muxing without
// re-encoding. It returns the number of seconds removed.
//
// The original is only replaced once the new file has been written in full and
// probed, so a failure anywhere leaves the episode playable.
func CutStreamCopy(path string, spans []Span, total float64) (float64, error) {
	ranges := keepRanges(spans, total)
	if len(ranges) == 0 {
		return 0, fmt.Errorf("refusing to remove the entire episode")
	}
	var keepSecs float64
	for _, r := range ranges {
		keepSecs += r[1] - r[0]
	}

	work, err := os.MkdirTemp(filepath.Dir(path), ".adtext")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(work)

	ext := filepath.Ext(path)
	var parts []string
	for i, r := range ranges {
		part := filepath.Join(work, fmt.Sprintf("p%03d%s", i, ext))
		// -ss before -i seeks the demuxer (fast); -t after -i is then relative
		// to that seek point. -map 0:a:0 and -vn drop any cover-art stream,
		// which the concat demuxer would otherwise refuse to join.
		args := []string{"-v", "error", "-ss", ftoa(r[0])}
		if d := r[1] - r[0]; r[1] < total-0.05 {
			args = append(args, "-t", ftoa(d))
		}
		args = append(args, "-i", path, "-map", "0:a:0", "-vn",
			"-c", "copy", "-y", part)
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			return 0, fmt.Errorf("extracting %.0fs-%.0fs: %v (%s)",
				r[0], r[1], err, strings.TrimSpace(string(out)))
		}
		parts = append(parts, part)
	}

	out := path + ".adtext" + ext
	defer os.Remove(out)
	if len(parts) == 1 {
		if err := os.Rename(parts[0], out); err != nil {
			return 0, err
		}
	} else {
		list := filepath.Join(work, "list.txt")
		var b strings.Builder
		for _, p := range parts {
			// the concat demuxer takes a quoted path; single quotes inside a
			// path would break it, and our names are generated, so this is safe
			b.WriteString("file '" + p + "'\n")
		}
		if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
			return 0, err
		}
		args := []string{"-v", "error", "-f", "concat", "-safe", "0",
			"-i", list, "-c", "copy"}
		if strings.EqualFold(ext, ".m4a") || strings.EqualFold(ext, ".mp4") ||
			strings.EqualFold(ext, ".m4b") {
			// put the index at the front: players (Apple Podcasts especially)
			// need it before they will start a progressive download
			args = append(args, "-movflags", "+faststart")
		}
		args = append(args, "-y", out)
		if o, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			return 0, fmt.Errorf("joining %d piece(s): %v (%s)",
				len(parts), err, strings.TrimSpace(string(o)))
		}
	}

	// Sanity-check the result before it replaces anything: the duration must be
	// close to the sum of the kept ranges. A wildly different number means the
	// mux went wrong, and shipping it would corrupt the episode.
	got, err := ProbeDuration(out)
	if err != nil {
		return 0, err
	}
	if diff := got - keepSecs; diff > 5 || diff < -5 {
		return 0, fmt.Errorf("result is %.0fs but %.0fs was expected — not replacing the episode", got, keepSecs)
	}
	if err := os.Rename(out, path); err != nil {
		return 0, err
	}
	return total - got, nil
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }
