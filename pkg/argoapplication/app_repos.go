package argoapplication

import (
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// AppRepoEntry names an (intermediate) chart source in the app-of-apps chain.
// Apps whose spec.source matches an entry bypass the depth-1 lazy-skip in
// reposerverextract/appofapps.go, keeping the traversal chain alive even when
// the intermediate's spec is YAML-identical between branches and its source is
// external to --repo. The motivating case is a resource-repo PR whose path to
// the PR target threads through Applications sourced in other repos (e.g. a
// medical-envs App sourced from live-apps that emits medical-stack Apps
// sourced from padoa-helm-repo).
//
// Path empty = wildcard (any path in the repo). Path non-empty = exact match
// after CanonicalPath normalisation.
type AppRepoEntry struct {
	Repo string
	Path string
}

func (e AppRepoEntry) String() string {
	if e.Path == "" {
		return e.Repo
	}
	return e.Repo + ":" + e.Path
}

// CanonicalPath normalises a source path: strips a leading "./" and trims
// surrounding slashes. "", ".", "./", "/" all canonicalise to "".
func CanonicalPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// repoURLPattern extracts "owner/repo" from either git@ or https URLs.
// Mirrors the Padoa snapshot's normalize_repo so both sides agree on the key.
var repoURLPattern = regexp.MustCompile(`^(?:git@github\.com[:/]|https?://github\.com/)([^/]+)/([^/]+?)(?:\.git)?/?$`)

// NormalizeRepoURL converts "git@github.com:padoa/foo.git" → "padoa/foo".
// Returns the input verbatim if it doesn't look like a GitHub URL.
func NormalizeRepoURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	m := repoURLPattern.FindStringSubmatch(u)
	if m == nil {
		return u
	}
	return m[1] + "/" + m[2]
}

// SourceMatchesAppRepos returns true if any source in the Application's spec
// matches an entry in the list. Checks both spec.source (single-source) and
// spec.sources[*] (multi-source Applications).
func SourceMatchesAppRepos(yaml *unstructured.Unstructured, entries []AppRepoEntry) bool {
	if yaml == nil || len(entries) == 0 {
		return false
	}

	type src struct{ repo, path string }
	var sources []src

	if repoURL, found, _ := unstructured.NestedString(yaml.Object, "spec", "source", "repoURL"); found {
		path, _, _ := unstructured.NestedString(yaml.Object, "spec", "source", "path")
		sources = append(sources, src{NormalizeRepoURL(repoURL), CanonicalPath(path)})
	}
	if slice, found, _ := unstructured.NestedSlice(yaml.Object, "spec", "sources"); found {
		for _, s := range slice {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			repoURL, _, _ := unstructured.NestedString(m, "repoURL")
			if repoURL == "" {
				continue
			}
			path, _, _ := unstructured.NestedString(m, "path")
			sources = append(sources, src{NormalizeRepoURL(repoURL), CanonicalPath(path)})
		}
	}

	for _, s := range sources {
		for _, e := range entries {
			if e.Repo != s.repo {
				continue
			}
			if e.Path == "" || e.Path == s.path {
				return true
			}
		}
	}
	return false
}
