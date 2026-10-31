package adtext

// Keeping the transcripts we already paid for, so a rules change can be
// replayed against real episodes instead of hand-written ones.
//
// The repeated failure in this feature has not been bad rules, it has been bad
// verification: five times a change passed its tests and still shipped broken,
// twice because a hand-written test did not exercise the path that breaks (a
// four-mention brand test could not catch a six-mention bug) and once because
// the pass was never called at all. Hand-written fixtures encode what I
// already understood; the library encodes what actually happens.
//
// Each record is the transcript plus the spans that were cut under a known
// rules revision. cmd/replay re-runs the current rules over all of them and
// reports what changed, which makes "nothing that used to be found is lost" a
// check rather than a hope.

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Record is one episode's transcript and what was cut from it.
type Record struct {
	VideoID string    `json:"video_id"`
	Podcast string    `json:"podcast"`
	Title   string    `json:"title"`
	Total   float64   `json:"total"`
	Rev     int       `json:"rules_revision"`
	When    time.Time `json:"when"`
	// The show's own names, stored with the record because the rules need them
	// to tell a Patreon plug from an advertiser. Replaying without them gave
	// different spans from the same input and reported a phantom change.
	SelfHosts []string  `json:"self_hosts"`
	Spans     []Span    `json:"spans"`
	Segments  []Segment `json:"segments"`
}

// CorpusDir is where records live under the config directory.
func CorpusDir(configDir string) string {
	return filepath.Join(configDir, "adtext-corpus")
}

// corpusKeep bounds the corpus. Transcripts are ~100KB gzipped, so this is a
// few tens of megabytes for a couple of years of listening.
const corpusKeep = 400

// SaveRecord writes one episode to the corpus and prunes the oldest.
func SaveRecord(configDir string, r Record) error {
	dir := CorpusDir(configDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	r.When = time.Now()
	tmp := filepath.Join(dir, r.VideoID+".json.gz.tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if err := json.NewEncoder(zw).Encode(r); err != nil {
		zw.Close()
		f.Close()
		os.Remove(tmp)
		return err
	}
	zw.Close()
	f.Close()
	if err := os.Rename(tmp, filepath.Join(dir, r.VideoID+".json.gz")); err != nil {
		os.Remove(tmp)
		return err
	}
	pruneCorpus(dir)
	return nil
}

func pruneCorpus(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= corpusKeep {
		return
	}
	type ent struct {
		name string
		mod  time.Time
	}
	var all []ent
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if fi, err := e.Info(); err == nil {
			all = append(all, ent{e.Name(), fi.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	for _, e := range all[mini(len(all), corpusKeep):] {
		os.Remove(filepath.Join(dir, e.name))
	}
}

// LoadRecords reads every record in the corpus.
func LoadRecords(configDir string) ([]Record, error) {
	dir := CorpusDir(configDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range ents {
		if e.IsDir() || filepath.Ext(e.Name()) != ".gz" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			continue
		}
		var r Record
		if json.NewDecoder(zr).Decode(&r) == nil {
			out = append(out, r)
		}
		zr.Close()
		f.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.Before(out[j].When) })
	return out, nil
}
