package main

// Replays the current ad rules over every episode in the corpus and reports
// what changed against the spans that were actually cut at the time.
//
// This exists because the repeated failure in this feature has been
// verification, not detection: a change passes its own tests and ships broken.
// Hand-written fixtures encode what the author already understood. The corpus
// encodes what the library actually contains, including the shapes nobody
// anticipated.
//
//	replay            report changes; exit 1 if anything is LOST
//	replay -v         also list every episode
//
// Run it before deploying a rules change.

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"ikoyhn/podcast-sponsorblock/internal/services/adtext"
)

// a span is "the same" if both edges moved less than this
const sameEdge = 5.0

func overlaps(a, b adtext.Span) bool {
	return a.Start < b.End && b.Start < a.End
}

func main() {
	dir := flag.String("config", "/config", "config directory holding adtext-corpus")
	verbose := flag.Bool("v", false, "list every episode")
	flag.Parse()

	recs, err := adtext.LoadRecords(*dir)
	if err != nil || len(recs) == 0 {
		fmt.Printf("no corpus in %s (%v)\n", adtext.CorpusDir(*dir), err)
		return
	}

	var lost, added, moved, same int
	type finding struct {
		ep   string
		kind string
		text string
	}
	var findings []finding

	for _, r := range recs {
		o := adtext.DefaultOptions()
		o.SelfHosts = r.SelfHosts
		now := adtext.FindAds(r.Segments, r.Total, o)
		label := r.VideoID
		if r.Title != "" {
			label = fmt.Sprintf("%s (%.40s)", r.VideoID, r.Title)
		}

		matchedNow := make([]bool, len(now))
		for _, was := range r.Spans {
			found := -1
			for i, is := range now {
				if !matchedNow[i] && overlaps(was, is) {
					found = i
					break
				}
			}
			if found < 0 {
				lost++
				findings = append(findings, finding{label, "LOST",
					fmt.Sprintf("%.0fs@%.0fs no longer found — %s", was.Seconds(), was.Start, was.Reason)})
				continue
			}
			matchedNow[found] = true
			is := now[found]
			if absf(is.Start-was.Start) > sameEdge || absf(is.End-was.End) > sameEdge {
				moved++
				findings = append(findings, finding{label, "moved",
					fmt.Sprintf("%.0f-%.0f -> %.0f-%.0f", was.Start, was.End, is.Start, is.End)})
			} else {
				same++
			}
		}
		for i, is := range now {
			if !matchedNow[i] {
				added++
				findings = append(findings, finding{label, "new",
					fmt.Sprintf("%.0fs@%.0fs — %s", is.Seconds(), is.Start, is.Reason)})
			}
		}
		if *verbose {
			fmt.Printf("  %-52s was %d now %d\n", label, len(r.Spans), len(now))
		}
	}

	sort.SliceStable(findings, func(i, j int) bool { return findings[i].kind < findings[j].kind })
	for _, f := range findings {
		fmt.Printf("%-6s %-52s %s\n", f.kind, f.ep, f.text)
	}
	fmt.Printf("\n%d episodes: %d unchanged, %d moved, %d new, %d LOST\n",
		len(recs), same, moved, added, lost)
	if lost > 0 {
		fmt.Println("\nLOST means the rules no longer find an ad they used to. Check each one.")
		os.Exit(1)
	}
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
