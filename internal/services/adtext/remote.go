package adtext

// Calling the optional GPU transcription sidecar.
//
// Kept optional on purpose: the sidecar needs glibc for the CUDA wheels while
// this image is Alpine, so it runs as its own container. If it is missing,
// stopped, or without a GPU, transcription falls back to the bundled
// whisper.cpp and the feature degrades rather than breaking.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ikoyhn/podcast-sponsorblock/internal/config"
)

// A 2.5-hour episode takes about two minutes on the GPU; allow generously for
// a cold model load and a queue behind another episode.
var remoteClient = &http.Client{Timeout: 30 * time.Minute}

func remoteURL() string { return strings.TrimSpace(os.Getenv("TRANSCRIBER_URL")) }

// transcribeRemote asks the sidecar for a transcript. The sidecar reads the
// audio from the same share, so only the path relative to the audio root
// crosses the wire.
func transcribeRemote(audioPath string) ([]Segment, error) {
	base := remoteURL()
	if base == "" {
		return nil, fmt.Errorf("no sidecar configured")
	}
	root, err := filepath.Abs(config.AppConfig.Setup.AudioDir)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(audioPath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("audio is outside the audio root")
	}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(base, "/")+"/transcribe?path="+url.QueryEscape(rel), nil)
	if err != nil {
		return nil, err
	}
	resp, err := remoteClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sidecar returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var w whisperOut
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	if len(w.Transcription) == 0 {
		return nil, fmt.Errorf("sidecar returned an empty transcript")
	}
	segs := make([]Segment, 0, len(w.Transcription))
	for _, t := range w.Transcription {
		segs = append(segs, Segment{
			Start: hhmmss(t.Timestamps.From),
			End:   hhmmss(t.Timestamps.To),
			Text:  strings.TrimSpace(t.Text),
		})
	}
	return segs, nil
}

// Ready reports whether transcription can run at all: either a sidecar is
// configured, or the local model is present (downloading it if need be).
func Ready(configDir string) error {
	if remoteURL() != "" {
		return nil
	}
	if Available(configDir) {
		return nil
	}
	return EnsureModel(configDir)
}
