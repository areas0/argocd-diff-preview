package argoapplication

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/dag-andersen/argocd-diff-preview/pkg/argocd"
	"github.com/dag-andersen/argocd-diff-preview/pkg/git"
	"github.com/dag-andersen/argocd-diff-preview/pkg/utils"
	"github.com/dag-andersen/argocd-diff-preview/pkg/vars"
)

// AppsOfAppsOptions controls Apps-of-Apps expansion behavior
type AppsOfAppsOptions struct {
	Enabled           bool
	MaxDepth          int
	TempFolder        string
	Debug             bool
	Repo              string
	RedirectRevisions []string
	FilterOptions     FilterOptions
	ArgoCDNamespace   string
	RunPrefix         string
	AllowedRootIDs    []string
}

// ExpandAppsOfAppsInBothBranches expands nested Applications/ApplicationSets discovered via parent Applications for both branches.
func ExpandAppsOfAppsInBothBranches(
	argo *argocd.ArgoCDInstallation,
	baseApps []ArgoResource,
	targetApps []ArgoResource,
	baseBranch *git.Branch,
	targetBranch *git.Branch,
	opts AppsOfAppsOptions,
) ([]ArgoResource, []ArgoResource, error) {
	if !opts.Enabled {
		return baseApps, targetApps, nil
	}

	log.Info().Msg("🤖 Expanding Applications of Applications (apps-of-apps)")

	if err := utils.CreateFolder(opts.TempFolder, true); err != nil {
		return nil, nil, fmt.Errorf("failed to create temp folder: %w", err)
	}

	baseExpanded, err := expandAppsOfApps(argo, baseApps, baseBranch, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to expand base apps-of-apps: %w", err)
	}

	targetExpanded, err := expandAppsOfApps(argo, targetApps, targetBranch, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to expand target apps-of-apps: %w", err)
	}

	return baseExpanded, targetExpanded, nil
}

// expandAppsOfApps discovers and flattens child Applications (and nested ApplicationSets) by rendering parent Applications in Argo CD.
func expandAppsOfApps(
	argo *argocd.ArgoCDInstallation,
	apps []ArgoResource,
	branch *git.Branch,
	opts AppsOfAppsOptions,
) ([]ArgoResource, error) {
	if len(apps) == 0 {
		return apps, nil
	}

	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 1
	}

	// BFS style expansion up to MaxDepth
	var result []ArgoResource
	queue := make([]ArgoResource, 0, len(apps))
	queue = append(queue, apps...)
	visited := map[string]bool{}
	for _, a := range apps {
		visited[a.Id] = true
		result = append(result, a)
	}

	// Per-level traversal
	depth := 0
	for len(queue) > 0 && depth < opts.MaxDepth {
		depthSize := len(queue)
		nextLevel := []ArgoResource{}

		for i := 0; i < depthSize; i++ {
			parent := queue[i]
			if parent.Kind != Application { // Only Applications can own child Applications in apps-of-apps
				continue
			}

			// If a specific root allowlist is provided, skip non-allowed roots
			if len(opts.AllowedRootIDs) > 0 {
				if !stringInSlice(parent.Id, opts.AllowedRootIDs) {
					log.Debug().Str("branch", branch.Name).Str("parent", parent.GetLongName()).Msg("Skipping parent — not in changed roots allowlist")
					continue
				}
			}

			// Apply a temporary, prefixed copy of the parent to render its child apps
			temp := *parent.Yaml.DeepCopy()
			// prefix name for uniqueness and label with runID for potential cleanup
			parentTmpName := buildPrefixedName(parent.Id, opts.RunPrefix, branch)
			temp.SetName(parentTmpName)
			ensureLabel(&temp, vars.ArgoCDApplicationLabelKey, opts.RunPrefix)

			// Apply and wait for readiness similar to extract flow
			if err := argo.K8sClient.ApplyManifest(&temp, "string", argo.Namespace); err != nil {
				log.Error().Err(err).Str("App", parent.GetLongName()).Msg("❌ Failed to apply parent application for discovery")
				continue
			}

			// wait until App is OutOfSync/Synced and manifests are available
			manifests, err := waitAndGetAppManifests(argo, parentTmpName, parent.GetLongName(), time.Duration(120)*time.Second)
			if err != nil {
				log.Error().Err(err).Str("App", parent.GetLongName()).Msg("❌ Failed to get manifests for discovery")
				// try to delete temp anyway
				_ = argo.K8sClient.DeleteArgoCDApplication(argo.Namespace, parentTmpName)
				continue
			}

			// Delete the temporary parent application to avoid clutter
			if err := argo.K8sClient.DeleteArgoCDApplication(argo.Namespace, parentTmpName); err != nil {
				log.Warn().Err(err).Str("App", parent.GetLongName()).Msg("⚠️ Failed to delete temporary parent application")
			}

			// Parse manifests and collect child Applications/ApplicationSets
			docs, err := parseYamlDocuments(manifests)
			if err != nil {
				log.Error().Err(err).Str("App", parent.GetLongName()).Msg("❌ Failed to parse discovered manifests")
				continue
			}

			// For each discovered doc, if Application or ApplicationSet, convert and patch
			var childAppSets []ArgoResource
			for _, doc := range docs {
				kind := doc.GetKind()
				if kind != "Application" && kind != "ApplicationSet" {
					continue
				}
				name := doc.GetName()
				if strings.TrimSpace(name) == "" {
					log.Warn().Str("Parent", parent.GetLongName()).Msg("⚠️ Discovered child without metadata.name; skipping")
					continue
				}

				// Make a deep copy to avoid aliasing
				docCopy := doc.DeepCopy()

				switch kind {
				case "Application":
					child := NewArgoResource(docCopy, Application, name, name, parent.FileName, branch.Type())
					// Patch just like other apps
					patched, err := PatchApplication(argo.Namespace, *child, branch, opts.Repo, opts.RedirectRevisions)
					if err != nil {
						log.Error().Err(err).Str("Child", child.GetLongName()).Msg("❌ Failed to patch discovered Application")
						continue
					}
					// Enqueue if new
					if !visited[patched.Id] {
						visited[patched.Id] = true
						result = append(result, *patched)
						nextLevel = append(nextLevel, *patched)
					}
				case "ApplicationSet":
					childAppSet := NewArgoResource(docCopy, ApplicationSet, name, name, parent.FileName, branch.Type())
					childAppSets = append(childAppSets, *childAppSet)
				}
			}

			// Convert any discovered ApplicationSets into Applications (local logic using argocd appset generate)
			if len(childAppSets) > 0 {
				appsFromSets, err := generateAppsFromAppSets(argo, childAppSets, branch, opts.TempFolder, opts.Debug)
				if err != nil {
					log.Error().Err(err).Str("Parent", parent.GetLongName()).Msg("❌ Failed to generate Applications from discovered ApplicationSets")
				} else {
					// Patch and enqueue
					appsFromSets, err = patchApplications(argo.Namespace, appsFromSets, branch, opts.Repo, opts.RedirectRevisions)
					if err != nil {
						log.Error().Err(err).Str("Parent", parent.GetLongName()).Msg("❌ Failed to patch Applications generated from ApplicationSets")
					} else {
						for _, a := range appsFromSets {
							if !visited[a.Id] {
								visited[a.Id] = true
								result = append(result, a)
								nextLevel = append(nextLevel, a)
							}
						}
					}
				}
			}
		}

		queue = nextLevel
		depth++
	}

	if depth > 0 {
		if len(opts.AllowedRootIDs) > 0 {
			log.Info().Str("branch", branch.Name).Msgf("🤖 Apps-of-apps expansion finished at depth %d; total applications: %d (gated by %d changed root(s))", depth, len(result), len(opts.AllowedRootIDs))
		} else {
			log.Info().Str("branch", branch.Name).Msgf("🤖 Apps-of-apps expansion finished at depth %d; total applications: %d", depth, len(result))
		}
	}

	// Final filtering pass if needed (keep behavior consistent with processAppSets)
	if len(result) > 0 {
		log.Debug().Str("branch", branch.Name).Msgf("🤖 Filtering %d Applications after expansion", len(result))
		result = FilterAll(result, opts.FilterOptions)
	}

	return result, nil
}

// buildPrefixedName builds a unique temporary name consistent with extract's prefixing scheme
func buildPrefixedName(id string, runPrefix string, branch *git.Branch) string {
	branchShort := "x" // unknown
	switch branch.Type() {
	case git.Base:
		branchShort = "b"
	case git.Target:
		branchShort = "t"
	}
	// Keep it simple; length will rarely overflow here as this is temporary
	return fmt.Sprintf("%s-%s-%s", runPrefix, branchShort, id)
}

// ensureLabel sets a label key/value on an unstructured object
func ensureLabel(obj *unstructured.Unstructured, key, value string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[key] = value
	obj.SetLabels(labels)
}

// waitAndGetAppManifests waits until an Application reaches OutOfSync/Synced and then returns manifests
func waitAndGetAppManifests(argo *argocd.ArgoCDInstallation, name string, logName string, timeout time.Duration) (string, error) {
	start := time.Now()
	for {
		if time.Since(start) > timeout {
			return "", fmt.Errorf("timed out waiting for application %s", logName)
		}

		output, err := argo.K8sClient.GetArgoCDApplication(argo.Namespace, name)
		if err != nil {
			return "", fmt.Errorf("failed to get application %s: %w", logName, err)
		}

		var appStatus struct {
			Status struct {
				Sync struct {
					Status string `yaml:"status"`
				} `yaml:"sync"`
			} `yaml:"status"`
		}
		if err := yaml.Unmarshal([]byte(output), &appStatus); err != nil {
			return "", fmt.Errorf("failed to parse application yaml for %s: %w", logName, err)
		}

		switch appStatus.Status.Sync.Status {
		case "OutOfSync", "Synced":
			retryCount := 5
			manifests, exists, err := argo.GetManifestsWithRetry(name, retryCount)
			if !exists {
				return "", fmt.Errorf("application %s does not exist", logName)
			}
			if err != nil {
				return "", fmt.Errorf("failed to get manifests for application %s: %w", logName, err)
			}
			return manifests, nil
		}

		time.Sleep(5 * time.Second)
	}
}

// parseYamlDocuments splits and unmarshals a YAML stream into k8s objects
func parseYamlDocuments(chunk string) ([]unstructured.Unstructured, error) {
	documentSeparator := regexp.MustCompile(`(?m)^---\s*$`)
	documents := documentSeparator.Split(chunk, -1)
	manifests := make([]unstructured.Unstructured, 0)

	for _, doc := range documents {
		trimmedDoc := strings.TrimSpace(doc)
		if trimmedDoc == "" {
			continue
		}
		var yamlObj map[string]interface{}
		if err := yaml.Unmarshal([]byte(trimmedDoc), &yamlObj); err != nil {
			return nil, fmt.Errorf("failed to parse YAML: %w", err)
		}
		if len(yamlObj) == 0 {
			continue
		}
		apiVersion, found, _ := unstructured.NestedString(yamlObj, "apiVersion")
		kind, kindFound, _ := unstructured.NestedString(yamlObj, "kind")
		if !found || !kindFound || apiVersion == "" || kind == "" {
			continue
		}
		manifests = append(manifests, unstructured.Unstructured{Object: yamlObj})
	}
	return manifests, nil
}

// generateAppsFromAppSets generates Applications from a list of ApplicationSets using argocd CLI
func generateAppsFromAppSets(
	argo *argocd.ArgoCDInstallation,
	appSets []ArgoResource,
	branch *git.Branch,
	tempFolder string,
	debug bool,
) ([]ArgoResource, error) {
	var outApps []ArgoResource
	if len(appSets) == 0 {
		return outApps, nil
	}

	for _, appSet := range appSets {
		// Write to temp file
		randomFileName, err := appSet.WriteToFolder(tempFolder)
		if err != nil {
			log.Error().Err(err).Str("branch", branch.Name).Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msgf("❌ Failed to write ApplicationSet to file")
			continue
		}

		if debug {
			if err := argo.EnsureArgoCdIsReady(); err != nil {
				return nil, fmt.Errorf("failed to wait for deployments to be ready: %w", err)
			}
		}

		// Generate applications using argocd appset generate
		retryCount := 5
		output, err := argo.AppsetGenerateWithRetry(randomFileName, retryCount)
		if err != nil {
			log.Error().Err(err).Str("branch", branch.Name).Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msg("❌ Failed to generate applications from ApplicationSet")
			return nil, err
		}

		if strings.TrimSpace(output) == "" || strings.TrimSpace(output) == "null" {
			log.Warn().Str("branch", branch.Name).Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msgf("⚠️ ApplicationSet generated empty output")
			continue
		}

		// parse output
		isList := strings.HasPrefix(output, "-")
		var yamlData []unstructured.Unstructured
		if isList {
			var yamlOutput []unstructured.Unstructured
			if err := yaml.Unmarshal([]byte(output), &yamlOutput); err != nil {
				log.Error().Str("branch", branch.Name).Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msg("❌ Failed to read output from ApplicationSet")
				log.Error().Err(err)
				continue
			}
			yamlData = yamlOutput
		} else {
			var yamlOutput unstructured.Unstructured
			if err := yaml.Unmarshal([]byte(output), &yamlOutput); err != nil {
				log.Error().Str("branch", branch.Name).Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msg("❌ Failed to read output from ApplicationSet")
				log.Error().Err(err)
				continue
			}
			yamlData = []unstructured.Unstructured{yamlOutput}
		}

		for _, doc := range yamlData {
			kind := doc.GetKind()
			if kind == "" {
				log.Error().Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msg("❌ Output from ApplicationSet contains no kind")
				continue
			}
			if kind != "Application" {
				log.Error().Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msg("❌ Output from ApplicationSet contains non-Application resources")
				continue
			}
			name := doc.GetName()
			if name == "" {
				log.Error().Str(appSet.Kind.ShortName(), appSet.GetLongName()).Msg("❌ Generated Application missing name")
				continue
			}
			docCopy := doc.DeepCopy()
			app := NewArgoResource(docCopy, Application, name, name, appSet.FileName, branch.Type())
			outApps = append(outApps, *app)
		}
	}
	return outApps, nil
}

// stringInSlice returns true if s is present in list
func stringInSlice(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
