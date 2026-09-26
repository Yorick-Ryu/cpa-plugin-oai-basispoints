package basispoints

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestVersionAndChangelogFollowReleasePolicy(t *testing.T) {
	pattern := `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(alpha|beta|rc)\.[1-9][0-9]*)?$`
	if !regexp.MustCompile(pattern).MatchString(Version) {
		t.Fatalf("version %q must follow docs/versioning.md", Version)
	}
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(changelog), "\n") {
		if strings.HasPrefix(line, "## ") {
			if fields := strings.Fields(line); len(fields) < 2 || fields[1] != "v"+Version {
				t.Fatalf("first changelog entry must describe v%s: %s", Version, line)
			}
			return
		}
	}
	t.Fatal("missing version changelog entry")
}
