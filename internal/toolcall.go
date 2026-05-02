package internal

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// ============================================================
// Data types
// ============================================================

type ParsedToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// ============================================================
// FormatOpenAIToolCalls — ported from toolcalls_format.go
// ============================================================

func FormatOpenAIToolCalls(calls []ParsedToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		args, _ := json.Marshal(c.Input)
		out = append(out, map[string]any{
			"id":   "call_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			"type": "function",
			"function": map[string]any{
				"name":      c.Name,
				"arguments": string(args),
			},
		})
	}
	return out
}

// Convert ParsedToolCall to ToolCall (our OpenAI streaming type)
func parsedToToolCall(c ParsedToolCall, index int) ToolCall {
	args, _ := json.Marshal(c.Input)
	return ToolCall{
		Index:    index,
		ID:       fmt.Sprintf("call_%s", uuid.New().String()[:12]),
		Type:     "function",
		Function: FunctionCall{Name: c.Name, Arguments: string(args)},
	}
}

// ============================================================
// BuildToolCallInstructions — ported from tool_prompt.go
// ============================================================

func BuildToolCallInstructions(toolNames []string) string {
	return `TOOL CALL FORMAT — FOLLOW EXACTLY:

<|DSML|tool_calls>
  <|DSML|invoke name="TOOL_NAME_HERE">
    <|DSML|parameter name="PARAMETER_NAME"><![CDATA[PARAMETER_VALUE]]></|DSML|parameter>
  </|DSML|invoke>
</|DSML|tool_calls>

RULES:
1) Use the <|DSML|tool_calls> wrapper format.
2) Put one or more <|DSML|invoke> entries under a single <|DSML|tool_calls> root.
3) Put the tool name in the invoke name attribute: <|DSML|invoke name="TOOL_NAME">.
4) All string values must use <![CDATA[...]]>, even short ones. This includes code, scripts, file contents, prompts, paths, names, and queries.
5) Every top-level argument must be a <|DSML|parameter name="ARG_NAME">...</|DSML|parameter> node.
6) Objects use nested XML elements inside the parameter body. Arrays may repeat <item> children.
7) Numbers, booleans, and null stay plain text.
8) Use only the parameter names in the tool schema. Do not invent fields.
9) Do NOT wrap XML in markdown fences. Do NOT output explanations, role markers, or internal monologue.
10) If you call a tool, the first non-whitespace characters of that tool block must be exactly <|DSML|tool_calls>.
11) Never omit the opening <|DSML|tool_calls> tag, even if you already plan to close with </|DSML|tool_calls>.
12) Compatibility note: the runtime also accepts the legacy XML tags <tool_calls> / <invoke> / <parameter>, but prefer the DSML-prefixed form above.

PARAMETER SHAPES:
- string => <|DSML|parameter name="x"><![CDATA[value]]></|DSML|parameter>
- object => <|DSML|parameter name="x"><field>...</field></|DSML|parameter>
- array => <|DSML|parameter name="x"><item>...</item><item>...</item></|DSML|parameter>
- number/bool/null => <|DSML|parameter name="x">plain_text</|DSML|parameter>

【WRONG — Do NOT do these】:

Wrong 1 — mixed text after XML:
  <|DSML|tool_calls>...</|DSML|tool_calls> I hope this helps.
Wrong 2 — Markdown code fences:
  ` + "```xml" + `
  <|DSML|tool_calls>...</|DSML|tool_calls>
  ` + "```" + `
Wrong 3 — missing opening wrapper:
  <|DSML|invoke name="TOOL_NAME">...</|DSML|invoke>
  </|DSML|tool_calls>

Remember: The ONLY valid way to use tools is the <|DSML|tool_calls>...</|DSML|tool_calls> block at the end of your response.

` + buildCorrectToolExamples(toolNames)
}

type promptToolExample struct {
	name   string
	params string
}

func buildCorrectToolExamples(toolNames []string) string {
	names := uniqueToolNames(toolNames)
	examples := make([]string, 0, 4)

	if single, ok := firstBasicExample(names); ok {
		examples = append(examples, "Example A — Single tool:\n"+renderToolExampleBlock([]promptToolExample{single}))
	}
	if parallel := firstNBasicExamples(names, 2); len(parallel) >= 2 {
		examples = append(examples, "Example B — Two tools in parallel:\n"+renderToolExampleBlock(parallel))
	}
	if nested, ok := firstNestedExample(names); ok {
		examples = append(examples, "Example C — Tool with nested XML parameters:\n"+renderToolExampleBlock([]promptToolExample{nested}))
	}
	if script, ok := firstScriptExample(names); ok {
		examples = append(examples, "Example D — Tool with long script using CDATA (RELIABLE FOR CODE/SCRIPTS):\n"+renderToolExampleBlock([]promptToolExample{script}))
	}

	if len(examples) == 0 {
		return ""
	}
	return "【CORRECT EXAMPLES】:\n\n" + strings.Join(examples, "\n\n") + "\n\n"
}

func uniqueToolNames(toolNames []string) []string {
	names := make([]string, 0, len(toolNames))
	seen := map[string]bool{}
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func firstBasicExample(names []string) (promptToolExample, bool) {
	for _, name := range names {
		if params, ok := exampleBasicParams(name); ok {
			return promptToolExample{name: name, params: params}, true
		}
	}
	return promptToolExample{}, false
}

func firstNBasicExamples(names []string, count int) []promptToolExample {
	out := make([]promptToolExample, 0, count)
	for _, name := range names {
		if params, ok := exampleBasicParams(name); ok {
			out = append(out, promptToolExample{name: name, params: params})
			if len(out) == count {
				return out
			}
		}
	}
	return out
}

func firstNestedExample(names []string) (promptToolExample, bool) {
	for _, name := range names {
		if params, ok := exampleNestedParams(name); ok {
			return promptToolExample{name: name, params: params}, true
		}
	}
	return promptToolExample{}, false
}

func firstScriptExample(names []string) (promptToolExample, bool) {
	for _, name := range names {
		if params, ok := exampleScriptParams(name); ok {
			return promptToolExample{name: name, params: params}, true
		}
	}
	return promptToolExample{}, false
}

func renderToolExampleBlock(calls []promptToolExample) string {
	var b strings.Builder
	b.WriteString("<|DSML|tool_calls>\n")
	for _, call := range calls {
		b.WriteString(`  <|DSML|invoke name="`)
		b.WriteString(call.name)
		b.WriteString(`">` + "\n")
		b.WriteString(indentPromptParameters(call.params, "    "))
		b.WriteString("\n  </|DSML|invoke>\n")
	}
	b.WriteString("</|DSML|tool_calls>")
	return b.String()
}

func indentPromptParameters(body, indent string) string {
	if strings.TrimSpace(body) == "" {
		return indent + `<|DSML|parameter name="content"></|DSML|parameter>`
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = line
			continue
		}
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

func wrapParameter(name, inner string) string {
	return `<|DSML|parameter name="` + name + `">` + inner + `</|DSML|parameter>`
}

func promptCDATA(text string) string {
	if text == "" {
		return ""
	}
	if strings.Contains(text, "]]>") {
		return "<![CDATA[" + strings.ReplaceAll(text, "]]>", "]]]]><![CDATA[>") + "]]>"
	}
	return "<![CDATA[" + text + "]]>"
}

func exampleBasicParams(name string) (string, bool) {
	switch strings.TrimSpace(name) {
	case "Read":
		return wrapParameter("file_path", promptCDATA("README.md")), true
	case "Glob":
		return wrapParameter("pattern", promptCDATA("**/*.go")) + "\n" + wrapParameter("path", promptCDATA(".")), true
	case "read_file":
		return wrapParameter("path", promptCDATA("src/main.go")), true
	case "list_files":
		return wrapParameter("path", promptCDATA(".")), true
	case "search_files":
		return wrapParameter("query", promptCDATA("tool call parser")), true
	case "Bash", "execute_command":
		return wrapParameter("command", promptCDATA("pwd")), true
	case "exec_command":
		return wrapParameter("cmd", promptCDATA("pwd")), true
	case "Write":
		return wrapParameter("file_path", promptCDATA("notes.txt")) + "\n" + wrapParameter("content", promptCDATA("Hello world")), true
	case "write_to_file":
		return wrapParameter("path", promptCDATA("notes.txt")) + "\n" + wrapParameter("content", promptCDATA("Hello world")), true
	case "Edit":
		return wrapParameter("file_path", promptCDATA("README.md")) + "\n" + wrapParameter("old_string", promptCDATA("foo")) + "\n" + wrapParameter("new_string", promptCDATA("bar")), true
	case "MultiEdit":
		return wrapParameter("file_path", promptCDATA("README.md")) + "\n" + `<|DSML|parameter name="edits"><item><old_string>` + promptCDATA("foo") + `</old_string><new_string>` + promptCDATA("bar") + `</new_string></item></|DSML|parameter>`, true
	}
	return "", false
}

func exampleNestedParams(name string) (string, bool) {
	switch strings.TrimSpace(name) {
	case "MultiEdit":
		return wrapParameter("file_path", promptCDATA("README.md")) + "\n" + `<|DSML|parameter name="edits"><item><old_string>` + promptCDATA("foo") + `</old_string><new_string>` + promptCDATA("bar") + `</new_string></item></|DSML|parameter>`, true
	case "Task":
		return wrapParameter("description", promptCDATA("Investigate flaky tests")) + "\n" + wrapParameter("prompt", promptCDATA("Run targeted tests and summarize failures")), true
	case "ask_followup_question":
		return wrapParameter("question", promptCDATA("Which approach do you prefer?")) + "\n" + `<|DSML|parameter name="follow_up"><item><text>` + promptCDATA("Option A") + `</text></item><item><text>` + promptCDATA("Option B") + `</text></item></|DSML|parameter>`, true
	}
	return "", false
}

func exampleScriptParams(name string) (string, bool) {
	scriptCommand := `cat > /tmp/test_escape.sh <<'EOF'
#!/bin/bash
echo 'single "double"'
echo "literal dollar: \$HOME"
EOF
bash /tmp/test_escape.sh`
	scriptContent := `#!/bin/bash
echo 'single "double"'
echo "literal dollar: $HOME"`

	switch strings.TrimSpace(name) {
	case "Bash":
		return wrapParameter("command", promptCDATA(scriptCommand)) + "\n" + wrapParameter("description", promptCDATA("Test shell escaping")), true
	case "execute_command":
		return wrapParameter("command", promptCDATA(scriptCommand)), true
	case "exec_command":
		return wrapParameter("cmd", promptCDATA(scriptCommand)), true
	case "Write":
		return wrapParameter("file_path", promptCDATA("test_escape.sh")) + "\n" + wrapParameter("content", promptCDATA(scriptContent)), true
	case "write_to_file":
		return wrapParameter("path", promptCDATA("test_escape.sh")) + "\n" + wrapParameter("content", promptCDATA(scriptContent)), true
	}
	return "", false
}

// ============================================================
// ParseToolCalls — ported from toolcalls_parse.go
// ============================================================

func ParseToolCalls(text string) []ParsedToolCall {
	return ParseToolCallsDetailed(text).Calls
}

type ToolCallParseResult struct {
	Calls             []ParsedToolCall
	SawToolCallSyntax bool
}

func ParseToolCallsDetailed(text string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text)
}

func parseToolCallsDetailedXMLOnly(text string) ToolCallParseResult {
	result := ToolCallParseResult{}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return result
	}
	result.SawToolCallSyntax = looksLikeToolCallSyntax(trimmed)
	trimmed = stripFencedCodeBlocks(trimmed)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return result
	}

	normalized, _ := normalizeDSMLToolCallMarkup(trimmed)
	parsed := parseXMLToolCalls(normalized)
	if len(parsed) == 0 && strings.Contains(strings.ToLower(normalized), "<![cdata[") {
		recovered := SanitizeLooseCDATA(normalized)
		if recovered != normalized {
			parsed = parseXMLToolCalls(recovered)
		}
	}
	if len(parsed) == 0 {
		return result
	}

	result.SawToolCallSyntax = true
	calls := filterToolCalls(parsed)
	result.Calls = calls
	return result
}

func filterToolCalls(parsed []ParsedToolCall) []ParsedToolCall {
	out := make([]ParsedToolCall, 0, len(parsed))
	for _, tc := range parsed {
		if tc.Name == "" {
			continue
		}
		if tc.Input == nil {
			tc.Input = map[string]any{}
		}
		out = append(out, tc)
	}
	return out
}

func looksLikeToolCallSyntax(text string) bool {
	hasDSML, hasCanonical := ContainsToolCallWrapperSyntaxOutsideIgnored(text)
	return hasDSML || hasCanonical
}

// ============================================================
// DSML normalization — ported from toolcalls_dsml.go
// ============================================================

func normalizeDSMLToolCallMarkup(text string) (string, bool) {
	if text == "" {
		return "", true
	}
	hasAlias, _ := ContainsToolMarkupSyntaxOutsideIgnored(text)
	if !hasAlias {
		return text, true
	}
	return rewriteDSMLToolMarkupOutsideIgnored(text), true
}

func rewriteDSMLToolMarkupOutsideIgnored(text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			b.WriteString(text[i:])
			break
		}
		if advanced {
			b.WriteString(text[i:next])
			i = next
			continue
		}
		tag, ok := scanToolMarkupTagAt(text, i)
		if !ok {
			b.WriteByte(text[i])
			i++
			continue
		}
		if tag.DSMLLike {
			b.WriteByte('<')
			if tag.Closing {
				b.WriteByte('/')
			}
			b.WriteString(tag.Name)
			b.WriteString(text[tag.NameEnd : tag.End+1])
			if text[tag.End] != '>' {
				b.WriteByte('>')
			}
			i = tag.End + 1
			continue
		}
		b.WriteString(text[tag.Start : tag.End+1])
		i = tag.End + 1
	}
	return b.String()
}

// ============================================================
// XML parsing — ported from toolcalls_parse_markup.go + toolcalls_xml.go
// ============================================================

var xmlAttrPattern = regexp.MustCompile(`(?is)\b([a-z0-9_:-]+)\s*=\s*("([^"]*)"|'([^']*)')`)

func parseXMLToolCalls(text string) []ParsedToolCall {
	wrappers := findXMLElementBlocks(text, "tool_calls")
	if len(wrappers) == 0 {
		repaired := repairMissingXMLToolCallsOpeningWrapper(text)
		if repaired != text {
			wrappers = findXMLElementBlocks(repaired, "tool_calls")
		}
	}
	if len(wrappers) == 0 {
		return nil
	}
	out := make([]ParsedToolCall, 0, len(wrappers))
	for _, wrapper := range wrappers {
		for _, block := range findXMLElementBlocks(wrapper.Body, "invoke") {
			call, ok := parseSingleXMLToolCall(block)
			if !ok {
				continue
			}
			out = append(out, call)
		}
	}
	return out
}

func repairMissingXMLToolCallsOpeningWrapper(text string) string {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "<tool_calls") {
		return text
	}
	var closePattern = regexp.MustCompile(`(?is)</tool_calls>`)
	var invokePattern = regexp.MustCompile(`(?is)<invoke\b[^>]*\bname\s*=\s*("([^"]*)"|'([^']*)')`)

	closeMatches := closePattern.FindAllStringIndex(text, -1)
	if len(closeMatches) == 0 {
		return text
	}
	invokeLoc := invokePattern.FindStringIndex(text)
	if invokeLoc == nil {
		return text
	}
	closeLoc := closeMatches[len(closeMatches)-1]
	if invokeLoc[0] >= closeLoc[0] {
		return text
	}
	return text[:invokeLoc[0]] + "<tool_calls>" + text[invokeLoc[0]:closeLoc[0]] + "</tool_calls>" + text[closeLoc[1]:]
}

type xmlElementBlock struct {
	Attrs string
	Body  string
	Start int
	End   int
}

func findXMLElementBlocks(text, tag string) []xmlElementBlock {
	if text == "" || tag == "" {
		return nil
	}
	var out []xmlElementBlock
	pos := 0
	for pos < len(text) {
		start, bodyStart, attrs, ok := findXMLStartTagOutsideCDATA(text, tag, pos)
		if !ok {
			break
		}
		closeStart, closeEnd, ok := findMatchingXMLEndTagOutsideCDATA(text, tag, bodyStart)
		if !ok {
			pos = bodyStart
			continue
		}
		out = append(out, xmlElementBlock{
			Attrs: attrs,
			Body:  text[bodyStart:closeStart],
			Start: start,
			End:   closeEnd,
		})
		pos = closeEnd
	}
	return out
}

func findXMLStartTagOutsideCDATA(text, tag string, from int) (start, bodyStart int, attrs string, ok bool) {
	lower := strings.ToLower(text)
	target := "<" + strings.ToLower(tag)
	for i := maxInt(from, 0); i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return -1, -1, "", false
		}
		if advanced {
			i = next
			continue
		}
		if strings.HasPrefix(lower[i:], target) && hasXMLTagBoundary(text, i+len(target)) {
			end := findXMLTagEnd(text, i+len(target))
			if end < 0 {
				return -1, -1, "", false
			}
			return i, end + 1, text[i+len(target) : end], true
		}
		i++
	}
	return -1, -1, "", false
}

func findMatchingXMLEndTagOutsideCDATA(text, tag string, from int) (closeStart, closeEnd int, ok bool) {
	lower := strings.ToLower(text)
	openTarget := "<" + strings.ToLower(tag)
	closeTarget := "</" + strings.ToLower(tag)
	depth := 1
	for i := maxInt(from, 0); i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return -1, -1, false
		}
		if advanced {
			i = next
			continue
		}
		if strings.HasPrefix(lower[i:], closeTarget) && hasXMLTagBoundary(text, i+len(closeTarget)) {
			end := findXMLTagEnd(text, i+len(closeTarget))
			if end < 0 {
				return -1, -1, false
			}
			depth--
			if depth == 0 {
				return i, end + 1, true
			}
			i = end + 1
			continue
		}
		if strings.HasPrefix(lower[i:], openTarget) && hasXMLTagBoundary(text, i+len(openTarget)) {
			end := findXMLTagEnd(text, i+len(openTarget))
			if end < 0 {
				return -1, -1, false
			}
			if !isSelfClosingXMLTag(text[:end]) {
				depth++
			}
			i = end + 1
			continue
		}
		i++
	}
	return -1, -1, false
}

func parseSingleXMLToolCall(block xmlElementBlock) (ParsedToolCall, bool) {
	attrs := parseXMLTagAttributes(block.Attrs)
	name := strings.TrimSpace(html.UnescapeString(attrs["name"]))
	if name == "" {
		return ParsedToolCall{}, false
	}

	inner := strings.TrimSpace(block.Body)
	// Try JSON body first
	if strings.HasPrefix(inner, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(inner), &payload); err == nil {
			input := map[string]any{}
			if params, ok := payload["input"].(map[string]any); ok {
				input = params
			}
			if len(input) == 0 {
				if params, ok := payload["parameters"].(map[string]any); ok {
					input = params
				}
			}
			return ParsedToolCall{Name: name, Input: input}, true
		}
	}

	// Parse parameter elements
	input := map[string]any{}
	for _, paramMatch := range findXMLElementBlocks(inner, "parameter") {
		paramAttrs := parseXMLTagAttributes(paramMatch.Attrs)
		paramName := strings.TrimSpace(html.UnescapeString(paramAttrs["name"]))
		if paramName == "" {
			continue
		}
		value := parseInvokeParameterValue(paramName, paramMatch.Body)
		appendMarkupValue(input, paramName, value)
	}

	if len(input) == 0 {
		if strings.TrimSpace(inner) != "" {
			return ParsedToolCall{}, false
		}
		return ParsedToolCall{Name: name, Input: map[string]any{}}, true
	}
	return ParsedToolCall{Name: name, Input: input}, true
}

func parseXMLTagAttributes(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, m := range xmlAttrPattern.FindAllStringSubmatch(raw, -1) {
		if len(m) < 5 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m[1]))
		if key == "" {
			continue
		}
		value := m[3]
		if value == "" {
			value = m[4]
		}
		out[key] = value
	}
	return out
}

func parseInvokeParameterValue(paramName, raw string) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	// CDATA extraction
	if value, ok := extractStandaloneCDATA(trimmed); ok {
		if parsed, ok := parseJSONLiteralValue(value); ok {
			return parsed
		}
		return value
	}
	// HTML entity decode
	decoded := html.UnescapeString(trimmed)
	// Try nested XML
	if strings.Contains(decoded, "<") && strings.Contains(decoded, ">") {
		if parsedValue, ok := parseXMLFragmentValue(decoded); ok {
			switch v := parsedValue.(type) {
			case map[string]any:
				if len(v) > 0 {
					return v
				}
			case []any:
				return v
			case string:
				if parsed, ok := parseJSONLiteralValue(strings.TrimSpace(v)); ok {
					return parsed
				}
				return v
			default:
				return v
			}
		}
		if kv := parseMarkupKVObject(decoded); len(kv) > 0 {
			return kv
		}
	}
	// JSON literal
	if parsed, ok := parseJSONLiteralValue(decoded); ok {
		return parsed
	}
	return decoded
}

func appendMarkupValue(out map[string]any, key string, value any) {
	if existing, ok := out[key]; ok {
		switch current := existing.(type) {
		case []any:
			out[key] = append(current, value)
		default:
			out[key] = []any{current, value}
		}
		return
	}
	out[key] = value
}

// ============================================================
// XML fragment parsing — ported from toolcalls_xml.go
// ============================================================

func parseXMLFragmentValue(raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	dec := xml.NewDecoder(strings.NewReader("<root>" + trimmed + "</root>"))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	start, ok := tok.(xml.StartElement)
	if !ok || !strings.EqualFold(start.Name.Local, "root") {
		return nil, false
	}
	value, err := parseXMLNodeValue(dec, start)
	if err != nil {
		return nil, false
	}
	return value, true
}

func parseXMLNodeValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	children := map[string]any{}
	var text strings.Builder
	hasChild := false

	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			s := string(t)
			if hasChild && strings.TrimSpace(s) == "" {
				continue
			}
			text.WriteString(s)
		case xml.StartElement:
			if !hasChild && strings.TrimSpace(text.String()) == "" {
				text.Reset()
			}
			hasChild = true
			child, err := parseXMLNodeValue(dec, t)
			if err != nil {
				return nil, err
			}
			appendXMLChildValue(children, t.Name.Local, child)
		case xml.EndElement:
			if len(children) == 0 {
				if parsed, ok := parseJSONLiteralValue(text.String()); ok {
					return parsed, nil
				}
				return text.String(), nil
			}
			if txt := text.String(); strings.TrimSpace(txt) != "" {
				if parsed, ok := parseJSONLiteralValue(txt); ok {
					children["_text"] = parsed
				} else {
					children["_text"] = txt
				}
			}
			if len(children) == 1 {
				if items, ok := children["item"]; ok {
					switch v := items.(type) {
					case []any:
						return v, nil
					default:
						return []any{v}, nil
					}
				}
			}
			return children, nil
		}
	}
}

func appendXMLChildValue(dst map[string]any, key string, value any) {
	if key == "" {
		return
	}
	if existing, ok := dst[key]; ok {
		switch current := existing.(type) {
		case []any:
			dst[key] = append(current, value)
		default:
			dst[key] = []any{current, value}
		}
		return
	}
	dst[key] = value
}

// ============================================================
// Markup helpers — ported from toolcalls_markup.go
// ============================================================

var toolCallMarkupKVPattern = regexp.MustCompile(`(?is)<(?:[a-z0-9_:-]+:)?([a-z0-9_\-.]+)\b[^>]*>(.*?)</(?:[a-z0-9_:-]+:)?([a-z0-9_\-.]+)>`)
var cdataPattern = regexp.MustCompile(`(?is)^<!\[CDATA\[(.*?)]]>$`)

func parseMarkupKVObject(text string) map[string]any {
	matches := toolCallMarkupKVPattern.FindAllStringSubmatch(strings.TrimSpace(text), -1)
	if len(matches) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		key := strings.TrimSpace(m[1])
		endKey := strings.TrimSpace(m[3])
		if key == "" || !strings.EqualFold(key, endKey) {
			continue
		}
		value := parseMarkupValue(m[2])
		if value == nil {
			continue
		}
		appendMarkupValue(out, key, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseMarkupValue(inner string) any {
	if value, ok := extractStandaloneCDATA(inner); ok {
		return value
	}
	value := strings.TrimSpace(html.UnescapeString(inner))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "<") && strings.Contains(value, ">") {
		if parsed := parseStructuredToolCallInput(value); len(parsed) > 0 {
			if len(parsed) == 1 {
				if raw, ok := parsed["_raw"].(string); ok {
					return raw
				}
			}
			return parsed
		}
	}
	var jsonValue any
	if json.Unmarshal([]byte(value), &jsonValue) == nil {
		return jsonValue
	}
	return value
}

func extractStandaloneCDATA(inner string) (string, bool) {
	trimmed := strings.TrimSpace(inner)
	if cdataMatches := cdataPattern.FindStringSubmatch(trimmed); len(cdataMatches) >= 2 {
		return cdataMatches[1], true
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "<![cdata[") {
		return trimmed[len("<![CDATA["):], true
	}
	return "", false
}

func parseJSONLiteralValue(raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}
	switch trimmed[0] {
	case '{', '[', '"', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 't', 'f', 'n':
	default:
		return nil, false
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

// SanitizeLooseCDATA repairs malformed CDATA sections
func SanitizeLooseCDATA(text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	const openMarker = "<![cdata["
	const closeMarker = "]]>"
	var b strings.Builder
	b.Grow(len(text))
	changed := false
	pos := 0
	for pos < len(text) {
		startRel := strings.Index(lower[pos:], openMarker)
		if startRel < 0 {
			b.WriteString(text[pos:])
			break
		}
		start := pos + startRel
		contentStart := start + len(openMarker)
		b.WriteString(text[pos:start])
		if endRel := strings.Index(lower[contentStart:], closeMarker); endRel >= 0 {
			end := contentStart + endRel + len(closeMarker)
			b.WriteString(text[start:end])
			pos = end
			continue
		}
		changed = true
		b.WriteString(text[contentStart:])
		pos = len(text)
	}
	if !changed {
		return text
	}
	return b.String()
}

func parseStructuredToolCallInput(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}
	}
	if strings.HasPrefix(trimmed, "<") {
		if parsed, ok := parseXMLFragmentValue(trimmed); ok {
			switch v := parsed.(type) {
			case map[string]any:
				if len(v) > 0 {
					return v
				}
				return map[string]any{}
			case string:
				if parsedText := parseToolCallInput(strings.TrimSpace(v)); len(parsedText) > 0 {
					return parsedText
				}
				return map[string]any{"_raw": v}
			}
		}
		if kv := parseMarkupKVObject(trimmed); len(kv) > 0 {
			return kv
		}
	}
	if kv := parseMarkupKVObject(trimmed); len(kv) > 0 {
		return kv
	}
	if parsed := parseToolCallInput(trimmed); len(parsed) > 0 {
		return parsed
	}
	return map[string]any{"_raw": html.UnescapeString(trimmed)}
}

// ============================================================
// Tool call input parsing — ported from toolcalls_input_parse.go
// ============================================================

func parseToolCallInput(v any) map[string]any {
	switch x := v.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		return x
	case string:
		raw := strings.TrimSpace(html.UnescapeString(x))
		if raw == "" {
			return map[string]any{}
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
			repairPathLikeControlChars(parsed)
			return parsed
		}
		repaired := repairInvalidJSONBackslashes(raw)
		if repaired != raw {
			if err := json.Unmarshal([]byte(repaired), &parsed); err == nil && parsed != nil {
				repairPathLikeControlChars(parsed)
				return parsed
			}
		}
		return map[string]any{"_raw": raw}
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return map[string]any{}
		}
		var parsed map[string]any
		if err := json.Unmarshal(b, &parsed); err == nil && parsed != nil {
			return parsed
		}
		return map[string]any{}
	}
}

func repairPathLikeControlChars(m map[string]any) {
	for k, v := range m {
		switch vv := v.(type) {
		case map[string]any:
			repairPathLikeControlChars(vv)
		case []any:
			for _, item := range vv {
				if child, ok := item.(map[string]any); ok {
					repairPathLikeControlChars(child)
				}
			}
		case string:
			if isPathLikeKey(k) && containsControlRune(vv) {
				m[k] = escapeControlRunes(vv)
			}
		}
	}
}

func isPathLikeKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(k, "path") || strings.Contains(k, "file")
}

func containsControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func escapeControlRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ============================================================
// JSON repair — ported from toolcalls_json_repair.go
// ============================================================

func repairInvalidJSONBackslashes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s) + 10)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			if i+1 < len(runes) {
				next := runes[i+1]
				switch next {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
					out.WriteRune('\\')
					out.WriteRune(next)
					i++
					continue
				case 'u':
					if i+5 < len(runes) {
						isHex := true
						for j := 1; j <= 4; j++ {
							r := runes[i+1+j]
							if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
								isHex = false
								break
							}
						}
						if isHex {
							out.WriteRune('\\')
							out.WriteRune('u')
							for j := 1; j <= 4; j++ {
								out.WriteRune(runes[i+1+j])
							}
							i += 5
							continue
						}
					}
				}
			}
			out.WriteString("\\\\")
		} else {
			out.WriteRune(runes[i])
		}
	}
	return out.String()
}

// ============================================================
// Tag scanning — ported from toolcalls_scan.go
// ============================================================

var toolMarkupNames = []string{"tool_calls", "invoke", "parameter"}

type ToolMarkupTag struct {
	Start       int
	End         int
	NameStart   int
	NameEnd     int
	Name        string
	Closing     bool
	SelfClosing bool
	DSMLLike    bool
	Canonical   bool
}

func ContainsToolMarkupSyntaxOutsideIgnored(text string) (hasDSML, hasCanonical bool) {
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.DSMLLike {
				hasDSML = true
			} else {
				hasCanonical = true
			}
			if hasDSML && hasCanonical {
				return
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return
}

func ContainsToolCallWrapperSyntaxOutsideIgnored(text string) (hasDSML, hasCanonical bool) {
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			return
		}
		if advanced {
			i = next
			continue
		}
		if tag, ok := scanToolMarkupTagAt(text, i); ok {
			if tag.Name != "tool_calls" {
				i = tag.End + 1
				continue
			}
			if tag.DSMLLike {
				hasDSML = true
			} else {
				hasCanonical = true
			}
			if hasDSML && hasCanonical {
				return
			}
			i = tag.End + 1
			continue
		}
		i++
	}
	return
}

func scanToolMarkupTagAt(text string, start int) (ToolMarkupTag, bool) {
	if start < 0 || start >= len(text) || text[start] != '<' {
		return ToolMarkupTag{}, false
	}
	lower := strings.ToLower(text)
	i := start + 1
	for i < len(text) && text[i] == '<' {
		i++
	}
	closing := false
	if i < len(text) && text[i] == '/' {
		closing = true
		i++
	}
	i, dsmlLike := consumeToolMarkupNamePrefix(lower, text, i)
	name, nameLen := matchToolMarkupName(lower, i)
	if nameLen == 0 {
		return ToolMarkupTag{}, false
	}
	nameEnd := i + nameLen
	for next, ok := consumeToolMarkupPipe(text, nameEnd); ok; next, ok = consumeToolMarkupPipe(text, nameEnd) {
		nameEnd = next
	}
	if !hasToolMarkupBoundary(text, nameEnd) {
		return ToolMarkupTag{}, false
	}
	end := findXMLTagEnd(text, nameEnd)
	if end < 0 {
		return ToolMarkupTag{}, false
	}
	trimmed := strings.TrimSpace(text[start : end+1])
	return ToolMarkupTag{
		Start:       start,
		End:         end,
		NameStart:   i,
		NameEnd:     nameEnd,
		Name:        name,
		Closing:     closing,
		SelfClosing: strings.HasSuffix(trimmed, "/>"),
		DSMLLike:    dsmlLike,
		Canonical:   !dsmlLike,
	}, true
}

func consumeToolMarkupNamePrefix(lower, text string, idx int) (int, bool) {
	dsmlLike := false
	for {
		next, ok := consumeToolMarkupNamePrefixOnce(lower, text, idx)
		if !ok {
			return idx, dsmlLike
		}
		idx = next
		dsmlLike = true
	}
}

func consumeToolMarkupNamePrefixOnce(lower, text string, idx int) (int, bool) {
	if next, ok := consumeToolMarkupPipe(text, idx); ok {
		return next, true
	}
	if idx < len(text) && (text[idx] == ' ' || text[idx] == '\t' || text[idx] == '\r' || text[idx] == '\n') {
		return idx + 1, true
	}
	if strings.HasPrefix(lower[idx:], "dsml") {
		return idx + len("dsml"), true
	}
	return idx, false
}

func matchToolMarkupName(lower string, start int) (string, int) {
	for _, name := range toolMarkupNames {
		if strings.HasPrefix(lower[start:], name) {
			return name, len(name)
		}
	}
	return "", 0
}

func consumeToolMarkupPipe(text string, idx int) (int, bool) {
	if idx >= len(text) {
		return idx, false
	}
	if text[idx] == '|' {
		return idx + 1, true
	}
	if strings.HasPrefix(text[idx:], "｜") {
		return idx + len("｜"), true
	}
	return idx, false
}

func hasToolMarkupBoundary(text string, idx int) bool {
	if idx >= len(text) {
		return true
	}
	switch text[idx] {
	case ' ', '\t', '\n', '\r', '>', '/':
		return true
	}
	return false
}

// ============================================================
// Common XML helpers
// ============================================================

func skipXMLIgnoredSection(lower string, i int) (next int, advanced bool, blocked bool) {
	switch {
	case strings.HasPrefix(lower[i:], "<![cdata["):
		end := strings.Index(lower[i+len("<![cdata["):], "]]>")
		if end < 0 {
			return 0, false, true
		}
		return i + len("<![cdata[") + end + len("]]>"), true, false
	case strings.HasPrefix(lower[i:], "<!--"):
		end := strings.Index(lower[i+len("<!--"):], "-->")
		if end < 0 {
			return 0, false, true
		}
		return i + len("<!--") + end + len("-->"), true, false
	}
	return i, false, false
}

func findXMLTagEnd(text string, from int) int {
	quote := byte(0)
	for i := maxInt(from, 0); i < len(text); i++ {
		ch := text[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '>' {
			return i
		}
	}
	return -1
}

func hasXMLTagBoundary(text string, idx int) bool {
	if idx >= len(text) {
		return true
	}
	switch text[idx] {
	case ' ', '\t', '\n', '\r', '>', '/':
		return true
	}
	return false
}

func isSelfClosingXMLTag(startTag string) bool {
	return strings.HasSuffix(strings.TrimSpace(startTag), "/")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ============================================================
// Fenced code block stripping — ported from toolcalls_parse.go
// ============================================================

func stripFencedCodeBlocks(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))
	lines := strings.SplitAfter(text, "\n")
	inFence := false
	fenceMarker := ""
	beforeFenceLen := 0
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !inFence {
			if marker, ok := parseFenceOpen(trimmed); ok {
				inFence = true
				fenceMarker = marker
				beforeFenceLen = b.Len()
				continue
			}
			b.WriteString(line)
			continue
		}
		if isFenceClose(trimmed, fenceMarker) {
			inFence = false
			fenceMarker = ""
		}
	}
	if inFence {
		result := b.String()
		if beforeFenceLen > 0 && beforeFenceLen <= len(result) {
			return result[:beforeFenceLen]
		}
		return ""
	}
	return b.String()
}

func parseFenceOpen(line string) (string, bool) {
	if len(line) < 3 {
		return "", false
	}
	ch := line[0]
	if ch != '`' && ch != '~' {
		return "", false
	}
	count := countLeadingFenceChars(line, ch)
	if count < 3 {
		return "", false
	}
	return strings.Repeat(string(ch), count), true
}

func isFenceClose(line, marker string) bool {
	if marker == "" || line == "" || line[0] != marker[0] {
		return false
	}
	count := countLeadingFenceChars(line, marker[0])
	if count < len(marker) {
		return false
	}
	return strings.TrimSpace(line[count:]) == ""
}

func countLeadingFenceChars(line string, ch byte) int {
	count := 0
	for count < len(line) && line[count] == ch {
		count++
	}
	return count
}
