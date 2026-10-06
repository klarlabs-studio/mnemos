// Package workflows guards the supply chain of the CI and release workflows.
package workflows

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var shaRef = regexp.MustCompile(`@[0-9a-f]{40}$`)

type workflow struct {
	Jobs map[string]struct {
		Uses  string `yaml:"uses"`
		Steps []struct {
			Name string `yaml:"name"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadWorkflows(t *testing.T) map[string]workflow {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows found (%v); the guard would check nothing", err)
	}
	out := map[string]workflow{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var w workflow
		if err := yaml.Unmarshal(data, &w); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out[filepath.Base(f)] = w
	}
	return out
}

// Every external action and reusable workflow is pinned to a full commit SHA.
// A tag or branch can be moved under us; release.yml can write releases and
// packages, and several workflows inherit secrets. Three references sat on
// @main (the shared go-ci and nox-remediate workflows, warden-verify), so
// whatever landed on those branches ran with this repo's permissions.
func TestWorkflows_PinEveryExternalReferenceToACommit(t *testing.T) {
	for file, w := range loadWorkflows(t) {
		check := func(where, ref string) {
			if ref == "" || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "docker://") {
				return
			}
			if !shaRef.MatchString(ref) {
				t.Errorf("%s %s: %q is not pinned to a 40-character commit SHA", file, where, ref)
			}
		}
		for name, job := range w.Jobs {
			check("job "+name, job.Uses)
			for i, s := range job.Steps {
				check("job "+name+" step "+stepName(s.Name, i), s.Uses)
			}
		}
	}
}

// A binary fetched from a release page is verified against a committed hash in
// the same step, before it runs. The release job used to curl nox and execute
// it with nothing checked.
func TestWorkflows_VerifyDownloadedBinaries(t *testing.T) {
	downloads := 0
	for file, w := range loadWorkflows(t) {
		for name, job := range w.Jobs {
			for i, s := range job.Steps {
				if !strings.Contains(s.Run, "releases/download") {
					continue
				}
				downloads++
				if !strings.Contains(s.Run, "sha256sum -c") {
					t.Errorf("%s job %s step %s downloads a release binary without `sha256sum -c` against a committed hash",
						file, name, stepName(s.Name, i))
				}
			}
		}
	}
	if downloads == 0 {
		t.Log("no release-binary downloads in any workflow")
	}
}

func stepName(name string, i int) string {
	if name != "" {
		return "\"" + name + "\""
	}
	return "#" + strconv.Itoa(i)
}
