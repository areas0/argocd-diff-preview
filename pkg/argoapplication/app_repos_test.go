package argoapplication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func TestCanonicalPath(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		".":                         "",
		"./":                        "",
		"/":                         "",
		" medical-stack ":           "medical-stack",
		"medical-stack":             "medical-stack",
		"./medical-stack":           "medical-stack",
		"/medical-stack":            "medical-stack",
		"medical-stack/":            "medical-stack",
		"./medical-stack/":          "medical-stack",
		"medical-stack/sub":         "medical-stack/sub",
		"./medical-stack/sub":       "medical-stack/sub",
		"/medical-stack/sub/":       "medical-stack/sub",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, CanonicalPath(in))
		})
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	cases := map[string]string{
		"":                                                  "",
		"git@github.com:padoa/foo.git":                      "padoa/foo",
		"git@github.com:padoa/foo":                          "padoa/foo",
		"git@github.com/padoa/foo":                          "padoa/foo", // seen in secrets YAMLs with / instead of :
		"https://github.com/padoa/foo.git":                  "padoa/foo",
		"https://github.com/padoa/foo":                      "padoa/foo",
		"https://github.com/padoa/foo/":                     "padoa/foo",
		"http://github.com/padoa/foo":                       "padoa/foo",
		"https://gitlab.com/other/repo":                     "https://gitlab.com/other/repo", // not GitHub → passthrough
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, NormalizeRepoURL(in))
		})
	}
}

func mustUnstructured(t *testing.T, y string) *unstructured.Unstructured {
	t.Helper()
	var u unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(y), &u); err != nil {
		t.Fatalf("parse yaml: %v", err)
	}
	return &u
}

func TestSourceMatchesAppRepos_SingleSource(t *testing.T) {
	app := mustUnstructured(t, `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: medical-envs}
spec:
  source:
    repoURL: git@github.com:padoa/live-apps.git
    path: dev-stateless-aks/medical-envs
`)

	cases := []struct {
		name    string
		entries []AppRepoEntry
		want    bool
	}{
		{"no entries → no match", nil, false},
		{"repo-only entry matches any path", []AppRepoEntry{{Repo: "padoa/live-apps"}}, true},
		{"repo:path exact match", []AppRepoEntry{{Repo: "padoa/live-apps", Path: "dev-stateless-aks/medical-envs"}}, true},
		{"repo:path non-matching path", []AppRepoEntry{{Repo: "padoa/live-apps", Path: "other"}}, false},
		{"different repo → no match", []AppRepoEntry{{Repo: "padoa/padoa-helm-repo"}}, false},
		{"non-canonical entry path matches after normalization", []AppRepoEntry{{Repo: "padoa/live-apps", Path: "/dev-stateless-aks/medical-envs/"}}, true}, // parseAppRepos feeds entries through CanonicalPath — slashy input canonicalises, then matches
		{"mixed list, one match", []AppRepoEntry{{Repo: "padoa/foo"}, {Repo: "padoa/live-apps"}}, true},
	}
	for _, tc := range cases {
		// Normalise entries through CanonicalPath like parseAppRepos would.
		normalised := make([]AppRepoEntry, len(tc.entries))
		for i, e := range tc.entries {
			normalised[i] = AppRepoEntry{Repo: e.Repo, Path: CanonicalPath(e.Path)}
		}
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SourceMatchesAppRepos(app, normalised))
		})
	}
}

func TestSourceMatchesAppRepos_MultiSource(t *testing.T) {
	app := mustUnstructured(t, `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: multi}
spec:
  sources:
    - repoURL: git@github.com:padoa/chart-a.git
      path: chart-a
    - repoURL: git@github.com:padoa/padoa-helm-repo.git
      path: medical-stack
`)

	assert.True(t, SourceMatchesAppRepos(app, []AppRepoEntry{{Repo: "padoa/padoa-helm-repo", Path: "medical-stack"}}))
	assert.True(t, SourceMatchesAppRepos(app, []AppRepoEntry{{Repo: "padoa/chart-a"}}))
	assert.False(t, SourceMatchesAppRepos(app, []AppRepoEntry{{Repo: "padoa/unrelated"}}))
}

func TestSourceMatchesAppRepos_NilYaml(t *testing.T) {
	assert.False(t, SourceMatchesAppRepos(nil, []AppRepoEntry{{Repo: "padoa/anything"}}))
}
