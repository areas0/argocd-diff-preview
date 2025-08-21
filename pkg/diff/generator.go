// Package diff generates human-friendly diffs between sets of rendered manifests.
package diff

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/go-git/go-git/v5/utils/merkletrie"
	"github.com/rs/zerolog/log"

	gitt "github.com/dag-andersen/argocd-diff-preview/pkg/git"
	"github.com/dag-andersen/argocd-diff-preview/pkg/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// AppInfo describes an application manifest artifact used to compute diffs.
type AppInfo struct {
	// Id holds the filename/id of the manifest artifact. Kept as 'Id' for backward compatibility.
	//lint:ignore ST1003 legacy field name; keep API compatibility
	Id          string //nolint:stylecheck,revive // legacy field name; keep API compatibility
	Name        string
	SourcePath  string
	FileContent string
}

// GenerateDiff generates a diff between base and target branches
func GenerateDiff(
	title string,
	outputFolder string,
	baseBranch *gitt.Branch,
	targetBranch *gitt.Branch,
	baseApps []AppInfo,
	targetApps []AppInfo,
	diffIgnoreRegex *string,
	lineCount uint,
	maxCharCount uint,
	timeInfo InfoBox,
	warnings []string,
) error {

	maxDiffMessageCharCount := maxCharCount
	if maxDiffMessageCharCount <= 0 {
		maxDiffMessageCharCount = 65536
	}

	log.Info().Msgf("🔮 Generating diff between %s and %s",
		baseBranch.Name, targetBranch.Name)

	// Set default context line count if not provided
	if lineCount <= 0 {
		lineCount = 3 // Default to 3 context lines if not specified
	}

	// Generate diffs using go-git by creating temporary git repos
	basePath := fmt.Sprintf("%s/%s", outputFolder, baseBranch.Type())
	targetPath := fmt.Sprintf("%s/%s", outputFolder, targetBranch.Type())
	summary, markdownFileSections, htmlFileSections, err := generateGitDiff(basePath, targetPath, diffIgnoreRegex, lineCount, baseApps, targetApps)
	if err != nil {
		return fmt.Errorf("failed to generate diff: %w", err)
	}

	infoBoxString := timeInfo.String()

	// Calculate the available space for the file sections
	remainingMaxChars := int(maxDiffMessageCharCount) - markdownTemplateLength() - len(summary) - len(infoBoxString) - len(title)
	if remainingMaxChars < 0 {
		remainingMaxChars = 0
	}

	// Warning message to be added if we need to truncate
	warningMessage := fmt.Sprintf("\n\n ⚠️⚠️⚠️ Diff is too long. Truncated to %d characters. This can be adjusted with the `--max-diff-length` flag",
		maxDiffMessageCharCount)

	// Concatenate file sections up to the max character limit
	var markdownCombinedDiff strings.Builder
	var htmlCombinedDiff strings.Builder
	var includedSections int

	// Calculate total size of all sections
	totalSize := 0
	for _, section := range markdownFileSections {
		totalSize += len(section)
	}

	// Check if truncation is needed
	if totalSize <= remainingMaxChars {
		// No truncation needed, include all sections
		for _, section := range markdownFileSections {
			markdownCombinedDiff.WriteString(section)
			includedSections++
		}
	} else {
		// Truncation needed
		log.Warn().Msgf("🚨 Diff is too long. Truncating message to %d characters", maxDiffMessageCharCount)

		currentSize := 0
		for i, section := range markdownFileSections {
			// Check if adding this section would exceed the limit (accounting for warning message)
			if currentSize+len(section) > remainingMaxChars-len(warningMessage) {
				// We can't add this full section
				// If this is the first section and empty builder, add a partial section
				if i == 0 && markdownCombinedDiff.Len() == 0 {
					// Add as much of the section as possible
					availableSpace := remainingMaxChars - len(warningMessage) - currentSize
					if availableSpace > 0 {
						markdownCombinedDiff.WriteString(section[:availableSpace])
					}
				}

				// Add warning and break
				markdownCombinedDiff.WriteString(warningMessage)
				break
			}

			// This section fits, add it
			markdownCombinedDiff.WriteString(section)
			includedSections++
			currentSize += len(section)
		}
	}

	// For HTML, all sections are included and the 'max-diff-length' option is ignored
	for _, section := range htmlFileSections {
		htmlCombinedDiff.WriteString(section)
	}

	// Generate and write markdown
	// Build warnings section
	var warningsMarkdown, warningsHTML string
	if len(warnings) > 0 {
		var b strings.Builder
		b.WriteString("\n\n## Warnings\n\n")
		for _, w := range warnings {
			b.WriteString("- ")
			b.WriteString(w)
			b.WriteString("\n")
		}
		warningsMarkdown = b.String()

		var hb strings.Builder
		hb.WriteString("\n\n<h2>Warnings</h2>\n<ul>\n")
		for _, w := range warnings {
			hb.WriteString("<li>")
			hb.WriteString(html.EscapeString(w))
			hb.WriteString("</li>\n")
		}
		hb.WriteString("</ul>\n")
		warningsHTML = hb.String()
	}

	markdown := printMarkdownDiff(
		title,
		strings.TrimSpace(summary),
		strings.TrimSpace(markdownCombinedDiff.String()),
		infoBoxString,
		warningsMarkdown,
	)
	markdownPath := fmt.Sprintf("%s/diff.md", outputFolder)
	if err := utils.WriteFile(markdownPath, markdown); err != nil {
		return fmt.Errorf("failed to write markdown: %w", err)
	}

	// Generate HTML
	htmlDiff := printHTMLDiff(
		title,
		strings.TrimSpace(summary),
		strings.TrimSpace(htmlCombinedDiff.String()),
		infoBoxString,
		warningsHTML,
	)
	htmlPath := fmt.Sprintf("%s/diff.html", outputFolder)
	if err := utils.WriteFile(htmlPath, htmlDiff); err != nil {
		return fmt.Errorf("failed to write html: %w", err)
	}

	log.Info().Msgf("🙏 Please check the %s file for differences", markdownPath)
	return nil
}

func writeManifestsToDisk(apps []AppInfo, folder string) error {
	if err := utils.CreateFolder(folder, true); err != nil {
		return fmt.Errorf("failed to create folder: %s: %w", folder, err)
	}
	for _, app := range apps {
		if err := utils.WriteFile(fmt.Sprintf("%s/%s", folder, app.Id), app.FileContent); err != nil {
			return fmt.Errorf("failed to write manifest %s: %w", app.Id, err)
		}
	}
	return nil
}

// generateGitDiff creates temporary Git repositories and uses go-git to generate a diff
func generateGitDiff(
	basePath, targetPath string,
	diffIgnore *string,
	diffContextLines uint,
	baseApps []AppInfo,
	targetApps []AppInfo,
) (string, []string, []string, error) {

	// Write base manifests to disk
	if err := writeManifestsToDisk(baseApps, basePath); err != nil {
		return "", nil, nil, fmt.Errorf("failed to write base manifests: %w", err)
	}

	// Write target manifests to disk
	if err := writeManifestsToDisk(targetApps, targetPath); err != nil {
		return "", nil, nil, fmt.Errorf("failed to write target manifests: %w", err)
	}

	baseAppsMap := make(map[string]AppInfo)
	for _, app := range baseApps {
		baseAppsMap[app.Id] = app
	}

	targetAppsMap := make(map[string]AppInfo)
	for _, app := range targetApps {
		targetAppsMap[app.Id] = app
	}

	// Create temporary directory for single Git repository
	repoPath, err := os.MkdirTemp("", "diff-repo-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to create temp dir for repo: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(repoPath); err != nil {
			log.Warn().Err(err).Msg("⚠️ Failed to remove temporary repo path")
		}
	}()

	// Initialize single Git repository
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to init repo: %w", err)
	}

	// Get worktree
	worktree, err := repo.Worktree()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get worktree: %w", err)
	}

	// Copy base files to repository and commit
	if err := copyFilesToRepo(basePath, repoPath); err != nil {
		return "", nil, nil, fmt.Errorf("failed to copy base files: %w", err)
	}

	if err := worktree.AddGlob("."); err != nil {
		return "", nil, nil, fmt.Errorf("failed to add base files to repo: %w", err)
	}

	author := &object.Signature{
		Name:  "ArgoCD Diff Preview",
		Email: "noreply@example.com",
		When:  time.Now(),
	}

	baseCommitHash, err := worktree.Commit("Base state", &git.CommitOptions{
		Author:            author,
		AllowEmptyCommits: true,
	})
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to commit base state: %w", err)
	}

	// Clear the working directory and copy target files
	if err := clearWorkingDirectory(repoPath); err != nil {
		return "", nil, nil, fmt.Errorf("failed to clear working directory: %w", err)
	}

	if err := copyFilesToRepo(targetPath, repoPath); err != nil {
		return "", nil, nil, fmt.Errorf("failed to copy target files: %w", err)
	}

	if err := worktree.AddGlob("."); err != nil {
		return "", nil, nil, fmt.Errorf("failed to add target files to repo: %w", err)
	}

	targetCommitHash, err := worktree.Commit("Target state", &git.CommitOptions{
		Author:            author,
		AllowEmptyCommits: true,
	})
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to commit target state: %w", err)
	}

	// Retrieve commits
	baseCommit, err := repo.CommitObject(baseCommitHash)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get base commit: %w", err)
	}

	targetCommit, err := repo.CommitObject(targetCommitHash)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get target commit: %w", err)
	}

	// Get base and target trees
	baseTree, err := baseCommit.Tree()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get base tree: %w", err)
	}

	targetTree, err := targetCommit.Tree()
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get target tree: %w", err)
	}

	// Compute diff between trees
	changes, err := baseTree.Diff(targetTree)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to compute diff: %w", err)
	}

	// Keep track of file paths by change type
	var changedFiles []Diff

	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to get change action: %w", err)
		}

		from, to, err := change.Files()
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to get files: %w", err)
		}

		diffContent := ""
		var oldContent, newContent string

		switch action {
		case merkletrie.Insert:

			if to != nil {
				blob, err := repo.BlobObject(to.Hash)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to get target blob: %w", err)
				}

				content, err := getBlobContent(blob)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to read target blob: %w", err)
				}

				newContent = content
				diffContent = formatNewFileDiff(content, diffContextLines, diffIgnore)
			}

		case merkletrie.Delete:

			if from != nil {
				blob, err := repo.BlobObject(from.Hash)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to get base blob: %w", err)
				}

				content, err := getBlobContent(blob)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to read base blob: %w", err)
				}

				oldContent = content
				diffContent = formatDeletedFileDiff(content, diffContextLines, diffIgnore)
			}

		case merkletrie.Modify:

			// Get content of both files and use the diff package
			var oldC, newC string

			if from != nil {
				blob, err := repo.BlobObject(from.Hash)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to get base blob: %w", err)
				}

				oldC, err = getBlobContent(blob)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to read base blob: %w", err)
				}
			}

			if to != nil {
				blob, err := repo.BlobObject(to.Hash)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to get target blob: %w", err)
				}

				newC, err = getBlobContent(blob)
				if err != nil {
					return "", nil, nil, fmt.Errorf("failed to read target blob: %w", err)
				}
			}

			// Use diff.Do to generate the diff
			oldContent = oldC
			newContent = newC
			diffContent = formatModifiedFileDiff(oldC, newC, diffContextLines, diffIgnore)
		}

		toName := ""
		fromName := ""
		if to != nil {
			toName = to.Name
		}
		if from != nil {
			fromName = from.Name
		}

		rd := buildResourceDiffs(oldContent, newContent, action, diffContextLines, diffIgnore)

		diff := Diff{
			newName:       targetAppsMap[toName].Name,
			oldName:       baseAppsMap[fromName].Name,
			newSourcePath: targetAppsMap[toName].SourcePath,
			oldSourcePath: baseAppsMap[fromName].SourcePath,
			action:        action,
			content:       diffContent,
			resourceDiffs: rd,
		}

		// If the diff didn't change and the names are the same, skip it
		if diff.content == "" && len(diff.resourceDiffs) == 0 && diff.oldName == diff.newName && diff.oldSourcePath == diff.newSourcePath {
			continue
		}

		// print diff
		log.Debug().
			Str("newName", diff.newName).
			Str("oldName", diff.oldName).
			Str("newSourcePath", diff.newSourcePath).
			Str("oldSourcePath", diff.oldSourcePath).
			Str("action", diff.action.String()).
			Msg("Found diff")

		changedFiles = append(changedFiles, diff)
	}

	if len(changedFiles) == 0 {
		return "No changes found", []string{"No changes found"}, []string{"No changes found"}, nil
	}

	// Build summary
	summary := buildSummary(changedFiles)

	// Create arrays of formatted file sections
	markdownFileSections := make([]string, 0, len(changedFiles))
	htmlFileSections := make([]string, 0, len(changedFiles))
	for _, diff := range changedFiles {

		// skips empty diffs
		if diff.content == "" && len(diff.resourceDiffs) == 0 {
			continue
		}

		// Get source path for this file, or use empty string if not found
		markdownFileSection := diff.buildMarkdownSection()
		markdownFileSections = append(markdownFileSections, markdownFileSection)

		htmlFileSection := diff.buildHTMLSection()
		htmlFileSections = append(htmlFileSections, htmlFileSection)
	}

	return summary, markdownFileSections, htmlFileSections, nil
}

// --- Resource level diff helpers ---

// parseManifests splits a YAML multi-doc stream into Kubernetes resources
func parseManifests(stream string) ([]unstructured.Unstructured, error) {
	// quick exit
	s := strings.TrimSpace(stream)
	if s == "" {
		return nil, nil
	}
	// Split on YAML document separator lines
	documentSeparator := regexp.MustCompile(`(?m)^---\s*$`)
	documents := documentSeparator.Split(stream, -1)
	manifests := make([]unstructured.Unstructured, 0, len(documents))
	for _, doc := range documents {
		trimmed := strings.TrimSpace(doc)
		if trimmed == "" {
			continue
		}
		var obj map[string]interface{}
		if err := yaml.Unmarshal([]byte(trimmed), &obj); err != nil {
			// Not a parseable YAML document; skip resource-level splitting
			return nil, fmt.Errorf("failed to parse YAML doc: %w", err)
		}
		if len(obj) == 0 {
			continue
		}
		// Validate as k8s resource
		apiVersion, _, _ := unstructured.NestedString(obj, "apiVersion")
		kind, _, _ := unstructured.NestedString(obj, "kind")
		if apiVersion == "" || kind == "" {
			// Not a k8s resource, skip
			continue
		}
		manifests = append(manifests, unstructured.Unstructured{Object: obj})
	}
	return manifests, nil
}

func makeResourceKey(u unstructured.Unstructured) resourceKey {
	name, _, _ := unstructured.NestedString(u.Object, "metadata", "name")
	ns, _, _ := unstructured.NestedString(u.Object, "metadata", "namespace")
	apiVersion, _, _ := unstructured.NestedString(u.Object, "apiVersion")
	kind, _, _ := unstructured.NestedString(u.Object, "kind")
	return resourceKey{APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name}
}

func marshalResourceYAML(u unstructured.Unstructured) string {
	b, err := yaml.Marshal(u.Object)
	if err != nil {
		return ""
	}
	return string(b)
}

// buildResourceDiffs computes resource-level diffs given old/new YAML streams and action
func buildResourceDiffs(oldContent, newContent string, action merkletrie.Action, diffContextLines uint, ignorePattern *string) []resourceDiff {
	// Try to parse; if parsing fails on one side, we'll return empty to fallback to file-level view
	switch action {
	case merkletrie.Insert:
		newRes, err := parseManifests(newContent)
		if err != nil || len(newRes) == 0 {
			return nil
		}
		rds := make([]resourceDiff, 0, len(newRes))
		for _, u := range newRes {
			yamlStr := marshalResourceYAML(u)
			rds = append(rds, resourceDiff{
				key:     makeResourceKey(u),
				action:  merkletrie.Insert,
				content: formatNewFileDiff(yamlStr, diffContextLines, ignorePattern),
			})
		}
		return rds
	case merkletrie.Delete:
		oldRes, err := parseManifests(oldContent)
		if err != nil || len(oldRes) == 0 {
			return nil
		}
		rds := make([]resourceDiff, 0, len(oldRes))
		for _, u := range oldRes {
			yamlStr := marshalResourceYAML(u)
			rds = append(rds, resourceDiff{
				key:     makeResourceKey(u),
				action:  merkletrie.Delete,
				content: formatDeletedFileDiff(yamlStr, diffContextLines, ignorePattern),
			})
		}
		return rds
	case merkletrie.Modify:
		oldRes, err1 := parseManifests(oldContent)
		newRes, err2 := parseManifests(newContent)
		if err1 != nil || err2 != nil {
			return nil
		}
		if len(oldRes) == 0 && len(newRes) == 0 {
			return nil
		}
		oldMap := map[resourceKey]string{}
		for _, u := range oldRes {
			oldMap[makeResourceKey(u)] = marshalResourceYAML(u)
		}
		newMap := map[resourceKey]string{}
		for _, u := range newRes {
			newMap[makeResourceKey(u)] = marshalResourceYAML(u)
		}
		// union of keys
		type pair struct {
			k resourceKey
			t string
		}
		keys := make([]pair, 0, len(oldMap)+len(newMap))
		seen := map[resourceKey]bool{}
		for k := range oldMap {
			keys = append(keys, pair{k, "o"})
			seen[k] = true
		}
		for k := range newMap {
			if !seen[k] {
				keys = append(keys, pair{k, "n"})
			}
		}
		// deterministic order: sort by String()
		sort.Slice(keys, func(i, j int) bool { return keys[i].k.String() < keys[j].k.String() })
		rds := make([]resourceDiff, 0, len(keys))
		for _, p := range keys {
			k := p.k
			oldY, oldOk := oldMap[k]
			newY, newOk := newMap[k]
			switch {
			case oldOk && !newOk:
				rds = append(rds, resourceDiff{key: k, action: merkletrie.Delete, content: formatDeletedFileDiff(oldY, diffContextLines, ignorePattern)})
			case !oldOk && newOk:
				rds = append(rds, resourceDiff{key: k, action: merkletrie.Insert, content: formatNewFileDiff(newY, diffContextLines, ignorePattern)})
			case oldOk && newOk:
				c := formatModifiedFileDiff(oldY, newY, diffContextLines, ignorePattern)
				if strings.TrimSpace(c) != "" {
					rds = append(rds, resourceDiff{key: k, action: merkletrie.Modify, content: c})
				}
			}
		}
		return rds
	default:
		return nil
	}
}

// getBlobContent reads the content of a Git blob
func getBlobContent(blob *object.Blob) (string, error) {
	reader, err := blob.Reader()
	if err != nil {
		return "", err
	}
	defer func() {
		if err := reader.Close(); err != nil {
			log.Warn().Err(err).Msg("⚠️ Failed to close blob reader")
		}
	}()

	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(reader); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// copyFilesToRepo copies files from source directory to destination Git repository
func copyFilesToRepo(srcDir, destDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(destDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(destPath, data, 0644)
	})
}

// clearWorkingDirectory removes all files and directories from the given path, but keeps the directory itself
func clearWorkingDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		// Skip .git directory to preserve Git repository structure
		if entry.Name() == ".git" {
			continue
		}

		entryPath := filepath.Join(path, entry.Name())
		if err := os.RemoveAll(entryPath); err != nil {
			return err
		}
	}

	return nil
}
