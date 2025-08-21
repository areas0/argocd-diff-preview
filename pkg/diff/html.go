package diff

import (
	"fmt"
	"html"
	"strings"
)

const htmlTemplate = `
<html>
<head>
<style>
body {
	font-family: arial;
}
.container {
	margin: auto;
	width: 910px;
}
.diffs {
	margin: 20px 0 20px 0;
}
.diff_container {
	width: 910px;
	overflow-x: scroll;
	border-radius: 8px;
	background:rgb(239, 239, 239);
	scrollbar-width: none;
	margin: 10px 0 10px 0;
}
table {
	font-family: monospace;
	border-spacing: 0px;
	width: 100%;
}
tr.normal_line {
	background:rgb(239, 239, 239);
}
tr.added_line {
	background:rgb(169, 216, 184);
}
tr.removed_line {
	background:rgb(247, 149, 173);
}
tr.comment_line {
	background:rgb(197, 194, 194);
}
pre {
	margin: 0;
	padding-left: 15px;
	padding-right: 15px;
}
</style>
</head>
<body>
<div class="container">
<h1>%title%</h1>

<div class="infobox">
<p><strong>Important notes</strong></p>
<ul>
	<li>This diff is a preview of rendered manifests; live clusters may have additional drift.</li>
	<li>If this PR isn't up to date with master, additional diffs may appear after merge.</li>
	<li>The tool is in beta; errors may occur.</li>
</ul>
</div>

<p>Summary:</p>
<pre>%summary%</pre>

<div class="diffs">
%app_diffs%
</div>

<pre>%info_box%</pre>
%warnings_section%
</div>
</body>
</html>
`

const htmlSection = `
<details>
<summary>
%header%
</summary>
<div class="diff_container">
<table>
	%rows%
</table>
</div>
</details>
`

const htmlLine = `<tr class="%s"><td><pre>%s</pre></td></tr>`

func printHTMLDiff(title, summary, diff string, infoBox string, warnings string) string {
	htmlDiff := strings.ReplaceAll(htmlTemplate, "%title%", title)
	htmlDiff = strings.ReplaceAll(htmlDiff, "%summary%", summary)
	htmlDiff = strings.ReplaceAll(htmlDiff, "%app_diffs%", diff)
	htmlDiff = strings.ReplaceAll(htmlDiff, "%info_box%", infoBox)
	htmlDiff = strings.ReplaceAll(htmlDiff, "%warnings_section%", warnings)
	return strings.TrimSpace(htmlDiff) + "\n"
}

func printHTMLSection(header string, commentHeader string, content string) string {
	s := strings.ReplaceAll(htmlSection, "%header%", html.EscapeString(header))
	rows := buildHTMLRows(commentHeader, content)
	s = strings.ReplaceAll(s, "%rows%", rows)
	return s
}

// printHTMLGroup renders a collapsible section that contains pre-rendered inner HTML blocks
// This is used to group multiple resource sections under one application header.
func printHTMLGroup(header string, innerHTML string) string {
	// For grouping, we reuse the details/summary but do not wrap with a diff table at the group level.
	var b strings.Builder
	b.WriteString("<details>\n<summary>\n")
	b.WriteString(html.EscapeString(header))
	b.WriteString("\n</summary>\n")
	b.WriteString(innerHTML)
	b.WriteString("\n</details>\n")
	return b.String()
}

// buildHTMLRows builds HTML table rows for the diff content with the comment header
func buildHTMLRows(commentHeader string, content string) string {
	rows := fmt.Sprintf(htmlLine, "comment_line", html.EscapeString(commentHeader))
	for _, line := range strings.Split(content, "\n") {
		if len(line) == 0 {
			rows += fmt.Sprintf(htmlLine, "normal_line", html.EscapeString(line))
			continue
		}
		switch line[0] {
		case '@':
			rows += fmt.Sprintf(htmlLine, "comment_line", html.EscapeString(line))
		case '-':
			rows += fmt.Sprintf(htmlLine, "removed_line", html.EscapeString(line))
		case '+':
			rows += fmt.Sprintf(htmlLine, "added_line", html.EscapeString(line))
		default:
			rows += fmt.Sprintf(htmlLine, "normal_line", html.EscapeString(line))
		}
	}
	return rows
}

// printHTMLBlock renders a non-collapsible diff block for a single resource
func printHTMLBlock(header string, commentHeader string, content string) string {
	var b strings.Builder
	b.WriteString("<h4>")
	b.WriteString(html.EscapeString(header))
	b.WriteString("</h4>\n")
	b.WriteString("<div class=\"diff_container\">\n<table>\n")
	b.WriteString(buildHTMLRows(commentHeader, content))
	b.WriteString("\n</table>\n</div>\n")
	return b.String()
}
