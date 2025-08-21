package diff

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/utils/merkletrie"
)

func buildSummary(changedFiles []Diff) string {
	var summaryBuilder strings.Builder

	addedCount := 0
	deletedCount := 0
	modifiedCount := 0

	for _, diff := range changedFiles {
		switch diff.action {
		case merkletrie.Insert:
			addedCount++
		case merkletrie.Delete:
			deletedCount++
		case merkletrie.Modify:
			modifiedCount++
		}
	}

	summaryBuilder.WriteString(fmt.Sprintf("Total: %d apps changed\n", addedCount+deletedCount+modifiedCount))

	if 0 < addedCount {
		summaryBuilder.WriteString(fmt.Sprintf("\nAdded (%d):\n", addedCount))
		for _, diff := range changedFiles {
			if diff.action == merkletrie.Insert {
				summaryBuilder.WriteString(fmt.Sprintf("+ %s\n", diff.prettyName()))
			}
		}
	}

	if 0 < deletedCount {
		summaryBuilder.WriteString(fmt.Sprintf("\nDeleted (%d):\n", deletedCount))
		for _, diff := range changedFiles {
			if diff.action == merkletrie.Delete {
				summaryBuilder.WriteString(fmt.Sprintf("- %s\n", diff.prettyName()))
			}
		}
	}

	if 0 < modifiedCount {
		summaryBuilder.WriteString(fmt.Sprintf("\nModified (%d):\n", modifiedCount))
		for _, diff := range changedFiles {
			if diff.action == merkletrie.Modify {
				summaryBuilder.WriteString(fmt.Sprintf("± %s\n", diff.prettyName()))
			}
		}
	}

	// Resource-level recap if available
	resAdded := 0
	resDeleted := 0
	resModified := 0
	for _, d := range changedFiles {
		for _, rd := range d.resourceDiffs {
			switch rd.action {
			case merkletrie.Insert:
				resAdded++
			case merkletrie.Delete:
				resDeleted++
			case merkletrie.Modify:
				resModified++
			}
		}
	}
	if resAdded+resDeleted+resModified > 0 {
		summaryBuilder.WriteString("\nResource recap:\n")
		summaryBuilder.WriteString(fmt.Sprintf("  Added: %d\n", resAdded))
		summaryBuilder.WriteString(fmt.Sprintf("  Deleted: %d\n", resDeleted))
		summaryBuilder.WriteString(fmt.Sprintf("  Modified: %d\n", resModified))
	}

	// Global deletion warning
	if anyDeletion(changedFiles) {
		summaryBuilder.WriteString("\n> ⚠️ This diff includes deletions (applications or resources). Be extra careful when reviewing it.\n")
	}

	return summaryBuilder.String()
}

// anyDeletion returns true if any app or resource deletion is present.
func anyDeletion(diffs []Diff) bool {
	for i := range diffs {
		if diffs[i].HasDeletion() {
			return true
		}
	}
	return false
}

// Markdown summary builder
func buildSummaryMarkdown(changedFiles []Diff) string {
	var b strings.Builder

	// ...existing code building the summary...

	// Global deletion warning
	if anyDeletion(changedFiles) {
		b.WriteString("\n> ⚠️ This diff includes deletions (applications or resources).\n")
	}

	return b.String()
}

// HTML summary builder
func buildSummaryHTML(changedFiles []Diff) string {
	var b strings.Builder

	// ...existing code building the summary...

	// Global deletion warning
	if anyDeletion(changedFiles) {
		b.WriteString(`<p><strong>Warning:</strong> This diff includes deletions (applications or resources).</p>` + "\n")
	}

	return b.String()
}
