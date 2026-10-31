package cmd

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionFromBuildInfo(t *testing.T) {
	for _, tc := range []struct {
		name, override, want string
		info                 *debug.BuildInfo
	}{
		{"override", "v1.2.3", "v1.2.3", nil},
		{"missing", "", "devel", nil},
		{"release", "", "v0.0.4", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.4"}}},
		{"local tag dirty", "", "v0.0.4+dirty", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.4+dirty"}}},
		{"local", "", "devel (123456789abc)", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "123456789abcdef"}}}},
		{"dirty", "", "devel (abc+dirty)", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}, {Key: "vcs.modified", Value: "true"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionFromBuildInfo(tc.override, tc.info); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHelpAndVersionShowVersion(t *testing.T) {
	old := Version
	Version = "v9.8.7"
	t.Cleanup(func() { Version = old })
	for _, args := range [][]string{{"--help"}, {"help"}, {"--version"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			root := newRootCommand()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(args)
			if err := execute(root); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "v9.8.7") {
				t.Fatalf("missing version in %q", out.String())
			}
		})
	}
}
