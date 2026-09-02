package amdl

import (
	"main/utils/structs"
	"os"
	"regexp"
)

var (
	forbiddenNames     = regexp.MustCompile(`[/\\<>:"|?*]`)
	dl_atmos           bool
	dl_aac             bool
	dl_select          bool
	artist_select      bool
	debug_mode         bool
	print_json         bool
	save_m3u8_playlist bool
	liteServerFlag     string
	alac_max           *int
	atmos_max          *int
	mv_max             *int
	mv_audio_type      *string
	aac_type           *string
	Config             structs.ConfigSet
	counter            structs.Counter
	okDict             = make(map[string][]int)
	AddedTracks        []AddedTrack
)

type AddedTrack struct {
	Path     string `json:"path"`
	Artist   string `json:"artist"`
	ArtistID string `json:"artist_id"`
	Album    string `json:"album"`
	Song     string `json:"song"`
}

// topLevelKeys returns the set of top-level YAML keys in data.

// contains reports whether item is present in slice.
func contains[T comparable](slice []T, item T) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func LimitString(s string) string {
	if len([]rune(s)) > Config.LimitMax {
		return string([]rune(s)[:Config.LimitMax])
	}
	return s
}

func fileExists(path string) (bool, error) {
	f, err := os.Stat(path)
	if err == nil {
		return !f.IsDir(), nil
	} else if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
