package collector

import (
	"cmp"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// ReplayInfo holds metadata about a local replay file.
type ReplayInfo struct {
	Filename string `json:"filename"`
	Session  *int   `json:"session"`
	SizeKB   int    `json:"size_kb"`
	Modified string `json:"modified"`
}

var sessionNumRe = regexp.MustCompile(`session-(\d+)`)

// CollectReplays scans docs/replays/ for session HTML files.
func CollectReplays(projectRoot string) []ReplayInfo {
	replaysDir := filepath.Join(projectRoot, "docs", "replays")
	entries, err := os.ReadDir(replaysDir)
	if err != nil {
		return nil
	}

	replays := make([]ReplayInfo, 0)
	for _, e := range entries {
		if e.IsDir() || !sessionNumRe.MatchString(e.Name()) || filepath.Ext(e.Name()) != ".html" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}

		var sessionNum *int
		if m := sessionNumRe.FindStringSubmatch(e.Name()); len(m) > 1 {
			if n, err := strconv.Atoi(m[1]); err == nil {
				sessionNum = &n
			}
		}

		replays = append(replays, ReplayInfo{
			Filename: e.Name(),
			Session:  sessionNum,
			SizeKB:   int(info.Size() / 1024),
			Modified: info.ModTime().Format(time.DateTime),
		})
	}

	// Sort by session number descending
	slices.SortFunc(replays, func(a, b ReplayInfo) int {
		sa, sb := 0, 0
		if a.Session != nil {
			sa = *a.Session
		}
		if b.Session != nil {
			sb = *b.Session
		}
		return cmp.Compare(sb, sa) // descending
	})

	return replays
}
