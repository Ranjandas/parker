package main

import (
	"html"
	"regexp"
	"strings"
)

// codeBlock renders a fenced code block in the site's terminal style, with
// light highlighting for YAML, shell sessions and HCL.
func codeBlock(lang, code string) string {
	code = strings.TrimSuffix(code, "\n")
	var b strings.Builder
	label := map[string]string{"yaml": "yaml", "yml": "yaml", "console": "terminal", "sh": "shell", "bash": "shell", "shell": "shell", "hcl": "hcl", "json": "json"}[lang]
	b.WriteString(`<div class="term code">`)
	if label != "" {
		b.WriteString(`<div class="term-bar"><span class="dots"><i></i><i></i><i></i></span>` + label + `</div>`)
	}
	b.WriteString(`<pre class="src"><code>`)
	for i, line := range strings.Split(code, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch lang {
		case "yaml", "yml":
			b.WriteString(yamlLine(line))
		case "console":
			b.WriteString(consoleLine(line))
		case "sh", "bash", "shell", "hcl":
			b.WriteString(commentLine(line, "#"))
		default:
			b.WriteString(html.EscapeString(line))
		}
	}
	b.WriteString(`</code></pre></div>`)
	return b.String()
}

func span(class, s string) string {
	return `<span class="` + class + `">` + html.EscapeString(s) + `</span>`
}

// splitComment cuts a trailing # comment that isn't inside quotes.
func splitComment(line string) (code, comment string) {
	inS, inD := false, false
	for i, r := range line {
		switch {
		case r == '\'' && !inD:
			inS = !inS
		case r == '"' && !inS:
			inD = !inD
		case r == '#' && !inS && !inD && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i], line[i:]
		}
	}
	return line, ""
}

var yamlTok = regexp.MustCompile(`("(?:[^"\\]|\\.)*"|'[^']*')|([A-Za-z_$][\w$.\-/]*|"[^"]*"):(?:\s|$)|(?:^|[\s,\[{])(-?\d[\d.]*(?:ms|s|m|h)?|true|false|null)(?:$|[\s,\]}])`)

func yamlLine(line string) string {
	code, comment := splitComment(line)
	var b strings.Builder
	last := 0
	for _, m := range yamlTok.FindAllStringSubmatchIndex(code, -1) {
		switch {
		case m[2] >= 0: // quoted string
			b.WriteString(html.EscapeString(code[last:m[2]]))
			b.WriteString(span("s", code[m[2]:m[3]]))
			last = m[3]
		case m[4] >= 0: // key
			b.WriteString(html.EscapeString(code[last:m[4]]))
			b.WriteString(span("k", code[m[4]:m[5]]))
			last = m[5]
		case m[6] >= 0: // number, bool, null
			b.WriteString(html.EscapeString(code[last:m[6]]))
			b.WriteString(span("n", code[m[6]:m[7]]))
			last = m[7]
		}
	}
	b.WriteString(html.EscapeString(code[last:]))
	if comment != "" {
		b.WriteString(span("c", comment))
	}
	return b.String()
}

// consoleLine: "$ cmd  # note" lines are commands; anything else is output.
func consoleLine(line string) string {
	if strings.HasPrefix(line, "$ ") {
		return span("p", "$ ") + commentLine(line[2:], "#")
	}
	if i := strings.Index(line, "$ "); i > 0 && i < 12 && !strings.ContainsAny(line[:i], " \t") {
		// host$ cmd, as in laptop$ parker status
		return span("c", line[:i]) + span("p", "$ ") + commentLine(line[i+2:], "#")
	}
	return html.EscapeString(line)
}

func commentLine(line, _ string) string {
	code, comment := splitComment(line)
	out := html.EscapeString(code)
	if comment != "" {
		out += span("c", comment)
	}
	return out
}
