package diff

import (
	"strings"
)

const markdownTemplate = `
## %title%

> Important notes
>
> - This diff is a preview of rendered manifests; live clusters may have additional drift.
> - It does not check for validity against resources. Sync of changes may result in failure.
> - If this PR isn't up to date with master, additional diffs may appear that are **not** a result of the changes in the PR. Please rebase it.
> - The full rendered manifests are available as artifacts.
> - The tool is in beta; errors may occur, if this is not a helm rendering issue, please report it.

Summary:
` + "```yaml" + `
%summary%
` + "```" + `

<details>
<summary>Details</summary>

%app_diffs%

</details>

%info_box%
%warnings_section%
`

func markdownTemplateLength() int {
	template := strings.ReplaceAll(markdownTemplate, "%summary%", "")
	template = strings.ReplaceAll(template, "%app_diffs%", "")
	template = strings.ReplaceAll(template, "%title%", "")
	template = strings.ReplaceAll(template, "%info_box%", "")
	template = strings.ReplaceAll(template, "%warnings_section%", "")
	return len(template)
}

func printMarkdownDiff(title, summary, diff string, infoBox string, warnings string) string {
	markdown := strings.ReplaceAll(markdownTemplate, "%title%", title)
	markdown = strings.ReplaceAll(markdown, "%summary%", summary)
	markdown = strings.ReplaceAll(markdown, "%app_diffs%", diff)
	markdown = strings.ReplaceAll(markdown, "%info_box%", infoBox)
	markdown = strings.ReplaceAll(markdown, "%warnings_section%", warnings)
	return strings.TrimSpace(markdown) + "\n"
}
