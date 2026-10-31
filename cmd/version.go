package cmd

import "runtime/debug"

// Version can be set at build time with -ldflags.
var Version string

func buildVersion() string {
	info, _ := debug.ReadBuildInfo()
	return versionFromBuildInfo(Version, info)
}

func versionFromBuildInfo(override string, info *debug.BuildInfo) string {
	if override != "" {
		return override
	}
	if info == nil {
		return "devel"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision == "" {
		return "devel"
	}
	if modified {
		revision += "+dirty"
	}
	return "devel (" + revision + ")"
}
