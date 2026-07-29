package version

import (
	"runtime/debug"
	"strings"
)

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func Current() string {
	v, _, _ := Build()
	return v
}

func Build() (version, commit, date string) {
	version, commit, date = Version, Commit, Date
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if (version == "" || version == "dev") && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if commit == "" || commit == "none" {
				commit = setting.Value
				if len(commit) > 12 {
					commit = commit[:12]
				}
			}
		case "vcs.time":
			if date == "" || date == "unknown" {
				date = setting.Value
			}
		}
	}
	return
}
