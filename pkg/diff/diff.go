package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// Diff represents a change detected for an application/manifests file.
type Diff struct {
	newName       string
	oldName       string
	newSourcePath string
	oldSourcePath string
	action        merkletrie.Action
	content       string
	// resourceDiffs holds diffs split per Kubernetes resource within this app/manifests file
	resourceDiffs []resourceDiff
}

// HasDeletion reports if this app diff includes any deletion (app or resource level).
func (d *Diff) HasDeletion() bool {
	if d.action == merkletrie.Delete {
		return true
	}
	for _, r := range d.resourceDiffs {
		if r.action == merkletrie.Delete {
			return true
		}
	}
	return false
}

func (d *Diff) prettyName() string {
	switch {
	case d.newName != "" && d.oldName != "" && d.newName != d.oldName:
		return fmt.Sprintf("%s -> %s", d.oldName, d.newName)
	case d.newName != "":
		return d.newName
	case d.oldName != "":
		return d.oldName
	default:
		return "Unknown"
	}
}

func (d *Diff) prettyPath() string {
	switch {
	case d.newSourcePath != "" && d.oldSourcePath != "" && d.newSourcePath != d.oldSourcePath:
		return fmt.Sprintf("%s -> %s", d.oldSourcePath, d.newSourcePath)
	case d.newSourcePath != "":
		return d.newSourcePath
	case d.oldSourcePath != "":
		return d.oldSourcePath
	default:
		return "Unknown"
	}
}

func (d *Diff) commentHeader() string {
	switch d.action {
	case merkletrie.Insert:
		return fmt.Sprintf("@@ Application added: %s (%s) @@\n", d.prettyName(), d.prettyPath())
	case merkletrie.Delete:
		return fmt.Sprintf("@@ Application deleted: %s (%s) @@\n", d.prettyName(), d.prettyPath())
	case merkletrie.Modify:
		return fmt.Sprintf("@@ Application modified: %s (%s) @@\n", d.prettyName(), d.prettyPath())
	default:
		return ""
	}
}

func (d *Diff) buildMarkdownSection() string {
	header := fmt.Sprintf("%s (%s)", d.prettyName(), d.prettyPath())

	// If we have resource-level diffs, render them as nested sections under this app
	if len(d.resourceDiffs) > 0 {
		return d.buildResourceMarkdownGroup(header)
	}

	// Fallback to legacy single block rendering
	content := strings.TrimSpace(fmt.Sprintf("%s%s", d.commentHeader(), d.content))
	return fmt.Sprintf("<details>\n<summary>%s</summary>\n<br>\n\n```diff\n%s\n```\n\n</details>\n\n", header, content)
}

func (d *Diff) buildHTMLSection() string {
	header := fmt.Sprintf("%s (%s)", d.prettyName(), d.prettyPath())
	if len(d.resourceDiffs) > 0 {
		return d.buildResourceHTMLGroup(header)
	}
	return printHTMLSection(header, d.commentHeader(), d.content)
}

// resourceKey uniquely identifies a Kubernetes resource
type resourceKey struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

func (rk resourceKey) String() string {
	ns := rk.Namespace
	if ns == "" {
		ns = "cluster"
	}
	// e.g., Deployment default/test-app (apps/v1)
	return fmt.Sprintf("%s %s/%s (%s)", rk.Kind, ns, rk.Name, rk.APIVersion)
}

// ShortRef returns a short reference without the Kind, suitable for cells in a Kind-grouped table.
func (rk resourceKey) ShortRef() string {
	ns := rk.Namespace
	if ns == "" {
		ns = "cluster"
	}
	// e.g., default/test-app (apps/v1) or cluster/my-crd (apiextensions.k8s.io/v1)
	return fmt.Sprintf("%s/%s (%s)", ns, rk.Name, rk.APIVersion)
}

// resourceDiff represents a diff for a single Kubernetes resource
type resourceDiff struct {
	key     resourceKey
	action  merkletrie.Action
	content string
}

func (rd *resourceDiff) commentHeader() string {
	switch rd.action {
	case merkletrie.Insert:
		return fmt.Sprintf("@@ Resource added: %s @@\n", rd.key.String())
	case merkletrie.Delete:
		return fmt.Sprintf("@@ Resource deleted: %s @@\n", rd.key.String())
	case merkletrie.Modify:
		return fmt.Sprintf("@@ Resource modified: %s @@\n", rd.key.String())
	default:
		return ""
	}
}

func (rd *resourceDiff) buildMarkdownSection() string {
	header := rd.key.String()
	content := strings.TrimSpace(fmt.Sprintf("%s%s", rd.commentHeader(), rd.content))
	return fmt.Sprintf("#### %s\n\n```diff\n%s\n```\n\n", header, content)
}

func (rd *resourceDiff) buildHTMLSection() string {
	header := rd.key.String()
	return printHTMLBlock(header, rd.commentHeader(), rd.content)
}

// Grouped rendering helpers for app-level resource listings
func (d *Diff) buildResourceMarkdownGroup(header string) string {
	// Build a quick summary by change type
	var added, modified, deleted []resourceDiff
	for _, r := range d.resourceDiffs {
		switch r.action {
		case merkletrie.Insert:
			added = append(added, r)
		case merkletrie.Modify:
			modified = append(modified, r)
		case merkletrie.Delete:
			deleted = append(deleted, r)
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("### %s\n\n", header))
	if ch := d.commentHeader(); ch != "" {
		b.WriteString("```diff\n")
		b.WriteString(strings.TrimSpace(ch))
		b.WriteString("\n```\n\n")
	}

	// Per-application precise summary grouped by resource type as a table
	b.WriteString("Resources changed (by type):\n\n")
	addIdx := indexByKind(added)
	modIdx := indexByKind(modified)
	delIdx := indexByKind(deleted)
	kinds := unionKinds(addIdx, modIdx, delIdx)

	b.WriteString("| Kind | Added | Modified | Deleted |\n")
	b.WriteString("|------|-------|----------|---------|\n")
	for _, kind := range kinds {
		a := len(addIdx[kind])
		m := len(modIdx[kind])
		dc := len(delIdx[kind])
		kindCell := fmt.Sprintf("%s — %s", kind, markdownKindBadge(a, m, dc))
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			kindCell,
			markdownCellList(addIdx[kind], false),
			markdownCellList(modIdx[kind], false),
			markdownCellList(delIdx[kind], true), // bold deleted items
		))
	}
	b.WriteString("\n")

	// Add a warning if there are deletions
	if len(deleted) > 0 {
		b.WriteString("> ⚠️ This change deletes resources in this application.\n\n")
	}

	// Collapsible sections by type, resource entries are not collapsible
	if len(added) > 0 {
		b.WriteString("<details>\n<summary>Added</summary>\n\n")
		for _, r := range added {
			b.WriteString(r.buildMarkdownSection())
		}
		b.WriteString("</details>\n\n")
	}
	if len(modified) > 0 {
		b.WriteString("<details>\n<summary>Modified</summary>\n\n")
		for _, r := range modified {
			b.WriteString(r.buildMarkdownSection())
		}
		b.WriteString("</details>\n\n")
	}
	if len(deleted) > 0 {
		b.WriteString("<details>\n<summary>Deleted</summary>\n\n")
		for _, r := range deleted {
			b.WriteString(r.buildMarkdownSection())
		}
		b.WriteString("</details>\n\n")
	}
	return b.String()
}

func (d *Diff) buildResourceHTMLGroup(header string) string {
	var added, modified, deleted []resourceDiff
	for _, r := range d.resourceDiffs {
		switch r.action {
		case merkletrie.Insert:
			added = append(added, r)
		case merkletrie.Modify:
			modified = append(modified, r)
		case merkletrie.Delete:
			deleted = append(deleted, r)
		}
	}
	var inner strings.Builder

	// Per-app summary grouped by resource type as an HTML table
	inner.WriteString("<h4>Resources changed (by type)</h4>\n")
	inner.WriteString(buildHTMLResourcesTable(added, modified, deleted))

	// Add a warning if there are deletions
	if len(deleted) > 0 {
		inner.WriteString(`<p><strong>Warning:</strong> This change deletes resources in this application.</p>` + "\n")
	}

	// Grouped by type, each resource as a non-collapsible block
	if len(added) > 0 {
		var g strings.Builder
		for _, r := range added {
			g.WriteString(r.buildHTMLSection())
		}
		inner.WriteString(printHTMLGroup("Added", g.String()))
	}
	if len(modified) > 0 {
		var g strings.Builder
		for _, r := range modified {
			g.WriteString(r.buildHTMLSection())
		}
		inner.WriteString(printHTMLGroup("Modified", g.String()))
	}
	if len(deleted) > 0 {
		var g strings.Builder
		for _, r := range deleted {
			g.WriteString(r.buildHTMLSection())
		}
		inner.WriteString(printHTMLGroup("Deleted", g.String()))
	}
	return printHTMLGroup(header, inner.String())
}

// --- helpers for grouped summaries ---

func indexByKind(list []resourceDiff) map[string][]resourceDiff {
	idx := make(map[string][]resourceDiff, len(list))
	for _, r := range list {
		idx[r.key.Kind] = append(idx[r.key.Kind], r)
	}
	// stable sort each bucket by namespace/name
	for k := range idx {
		sort.Slice(idx[k], func(i, j int) bool {
			return shortKey(idx[k][i]) < shortKey(idx[k][j])
		})
	}
	return idx
}

func unionKinds(maps ...map[string][]resourceDiff) []string {
	set := make(map[string]struct{})
	for _, m := range maps {
		for k := range m {
			set[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shortKey(r resourceDiff) string {
	ns := r.key.Namespace
	if ns == "" {
		ns = "cluster"
	}
	return ns + "/" + r.key.Name + " " + r.key.APIVersion
}

// Format a Markdown "badge" for counts in the Kind cell using colored emojis.
func markdownKindBadge(a, m, d int) string {
	// 🟢 added | 🟡 modified | 🔴 deleted
	return fmt.Sprintf("🟢%d - 🟡%d - 🔴%d", a, m, d)
}

// Format a Markdown table cell: one item per line; bold if requested (used for Deleted).
func markdownCellList(list []resourceDiff, bold bool) string {
	if len(list) == 0 {
		return "-"
	}
	items := make([]string, 0, len(list))
	for _, r := range list {
		ref := r.key.ShortRef()
		if bold {
			ref = "**" + ref + "**"
		}
		items = append(items, ref)
	}
	// One item per line in a Markdown table cell requires <br>
	return strings.Join(items, "<br>")
}

func buildHTMLResourcesTable(added, modified, deleted []resourceDiff) string {
	addIdx := indexByKind(added)
	modIdx := indexByKind(modified)
	delIdx := indexByKind(deleted)
	kinds := unionKinds(addIdx, modIdx, delIdx)

	var b strings.Builder
	b.WriteString(`<table>
  <thead>
    <tr><th>Kind</th><th>Added</th><th>Modified</th><th>Deleted</th></tr>
  </thead>
  <tbody>
`)
	for _, kind := range kinds {
		a := len(addIdx[kind])
		m := len(modIdx[kind])
		dc := len(delIdx[kind])
		b.WriteString("    <tr>")
		// Kind cell with colored counts
		b.WriteString("<td>")
		b.WriteString(kind)
		b.WriteString(" — ")
		b.WriteString(kindBadgeHTML(a, m, dc))
		b.WriteString("</td>")
		// Items (one per line), no counts in these cells
		b.WriteString("<td>")
		b.WriteString(htmlCellList(addIdx[kind], false))
		b.WriteString("</td>")
		b.WriteString("<td>")
		b.WriteString(htmlCellList(modIdx[kind], false))
		b.WriteString("</td>")
		b.WriteString("<td>")
		b.WriteString(htmlCellList(delIdx[kind], true)) // bold deleted items
		b.WriteString("</td>")
		b.WriteString("</tr>\n")
	}
	b.WriteString("  </tbody>\n</table>\n")
	return b.String()
}

// --- helpers for grouped summaries ---

// Format an HTML "badge" for counts in the Kind cell with colors.
func kindBadgeHTML(a, m, d int) string {
	// GitHub-like colors: green, yellow, red
	return fmt.Sprintf(
		`<span style="color:#1f883d">+%d</span> <span style="color:#9a6700">~%d</span> <span style="color:#cf222e">-%d</span>`,
		a, m, d,
	)
}

// Format an HTML table cell: one item per line via <br>; bold if requested (used for Deleted).
func htmlCellList(list []resourceDiff, bold bool) string {
	if len(list) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(list))
	for _, r := range list {
		ref := r.key.ShortRef()
		if bold {
			ref = "<strong>" + ref + "</strong>"
		}
		parts = append(parts, ref)
	}
	return strings.Join(parts, "<br>")
}
