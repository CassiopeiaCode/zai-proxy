package internal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/corpix/uarand"
	"github.com/google/uuid"
)

// getHTTPClient returns an HTTP client configured with proxy if HTTP_PROXY is set
func getHTTPClient() *http.Client {
	client := &http.Client{}
	
	if Cfg.HTTPProxy != "" {
		proxyURL, err := url.Parse(Cfg.HTTPProxy)
		if err != nil {
			LogError("Failed to parse HTTP_PROXY: %v", err)
		} else {
			transport := &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			}
			client.Transport = transport
			// Mask credentials in log message
			maskedURL := proxyURL.Scheme + "://"
			if proxyURL.User != nil {
				maskedURL += "***:***@"
			}
			maskedURL += proxyURL.Host
			LogDebug("Using HTTP proxy: %s", maskedURL)
		}
	}
	
	return client
}

func extractLatestUserContent(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			text, _ := messages[i].ParseContent()
			return text
		}
	}
	return ""
}

// 提取所有消息中的图片URL
func extractAllImageURLs(messages []Message) []string {
	var allImageURLs []string
	for _, msg := range messages {
		_, imageURLs := msg.ParseContent()
		allImageURLs = append(allImageURLs, imageURLs...)
	}
	return allImageURLs
}

// injectToolPrompt 将 tools 注入到消息列表中作为系统提示
func injectToolPrompt(messages []map[string]string, tools []Tool, toolChoice interface{}) []map[string]string {
	if len(tools) == 0 {
		return messages
	}

	var sb strings.Builder
	sb.WriteString("你只能通过调用函数来回答。你必须输出一个 JSON 代码块，格式如下：\n\n")
	sb.WriteString("<function_call>\n")
	sb.WriteString("{\"name\": \"函数名\", \"arguments\": {\"参数名\": \"参数值\"}}\n")
	sb.WriteString("</function_call>\n\n")
	sb.WriteString("严格遵循以下规则：\n")
	sb.WriteString("1. 不要输出任何解释、问候语或额外文字\n")
	sb.WriteString("2. 不要用 markdown 代码块包裹 <function_call>\n")
	sb.WriteString("3. 只能输出一个 <function_call> 块\n\n")
	sb.WriteString("可用的函数：\n\n")

	for _, t := range tools {
		fn := t.Function
		sb.WriteString(fmt.Sprintf("### %s\n", fn.Name))
		if fn.Description != "" {
			sb.WriteString(fmt.Sprintf("描述: %s\n", fn.Description))
		}
		if fn.Parameters != nil {
			paramsJSON, err := json.Marshal(fn.Parameters)
			if err == nil {
				sb.WriteString(fmt.Sprintf("参数: %s\n", string(paramsJSON)))
			}
		}
		sb.WriteString("\n")
	}

	// 检查 tool_choice 是否强制调用
	if tc, ok := toolChoice.(string); ok && tc == "required" {
		sb.WriteString("【重要】你必须调用上述函数之一，禁止输出普通文本回复。\n")
	}
	if tc, ok := toolChoice.(map[string]interface{}); ok {
		if fnObj, ok := tc["function"]; ok {
			if fnMap, ok := fnObj.(map[string]interface{}); ok {
				if name, ok := fnMap["name"].(string); ok {
					sb.WriteString(fmt.Sprintf("【重要】你必须调用函数 '%s'，禁止调用其他函数或输出文本。\n", name))
				}
			}
		}
	}

	// 拼接到最后一条用户消息末尾（比 system 消息对 GLM 更有效）
	if len(messages) > 0 {
		lastIdx := len(messages) - 1
		messages[lastIdx]["content"] = messages[lastIdx]["content"] + "\n\n" + sb.String()
	}

	return messages
}

// extractLatestUpstreamContent 提取最新用户消息内容（上游格式）
func extractLatestUpstreamContent(messages []map[string]string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			return messages[i]["content"]
		}
	}
	return ""
}

func makeUpstreamRequest(token string, messages []Message, model string, tools []Tool, toolChoice interface{}) (*http.Response, string, error) {
	payload, err := DecodeJWTPayload(token)
	if err != nil || payload == nil {
		return nil, "", fmt.Errorf("invalid token")
	}

	userID := payload.ID
	chatID := uuid.New().String()
	timestamp := time.Now().UnixMilli()
	requestID := uuid.New().String()
	userMsgID := uuid.New().String()

	targetModel := GetTargetModel(model)
	latestUserContent := extractLatestUserContent(messages)
	imageURLs := extractAllImageURLs(messages)

	signature := GenerateSignature(userID, requestID, latestUserContent, timestamp)

	url := fmt.Sprintf("https://chat.z.ai/api/v2/chat/completions?timestamp=%d&requestId=%s&user_id=%s&version=%s&platform=web&token=%s&current_url=%s&pathname=%s&signature_timestamp=%d",
		timestamp, requestID, userID, GetVersionNumber(), token,
		fmt.Sprintf("https://chat.z.ai/c/%s", chatID),
		fmt.Sprintf("/c/%s", chatID),
		timestamp)

	enableThinking := IsThinkingModel(model)
	autoWebSearch := IsSearchModel(model)
	// GLM-4.5-V 不支持 auto_web_search
	if targetModel == "glm-4.5v" {
		autoWebSearch = false
	}

	// 转换消息为上游格式
	var upstreamMessages []map[string]string
	for _, msg := range messages {
		upstreamMessages = append(upstreamMessages, msg.ToUpstreamMessage())
	}

	// 将 tools 注入到系统提示词中（在构造 body 之前）
	if len(tools) > 0 {
		upstreamMessages = injectToolPrompt(upstreamMessages, tools, toolChoice)
		latestUserContent = extractLatestUpstreamContent(upstreamMessages)
		signature = GenerateSignature(userID, requestID, latestUserContent, timestamp)
	}

	body := map[string]interface{}{
		"stream":           true,
		"model":            targetModel,
		"messages":         upstreamMessages,
		"signature_prompt": latestUserContent,
		"params":           map[string]interface{}{},
		"features": map[string]interface{}{
			"image_generation": false,
			"web_search":       false,
			"auto_web_search":  autoWebSearch,
			"preview_mode":     true,
			"enable_thinking":  enableThinking,
		},
		"chat_id": chatID,
		"id":      uuid.New().String(),
	}

	// 处理图片上传
	if len(imageURLs) > 0 {
		files, err := UploadImages(token, imageURLs)
		if err != nil {
			LogError("Failed to upload images: %v", err)
		}
		if len(files) > 0 {
			// 设置 ref_user_msg_id
			var filesData []map[string]interface{}
			for _, f := range files {
				fileMap := map[string]interface{}{
					"type":            f.Type,
					"file":            f.File,
					"id":              f.ID,
					"url":             f.URL,
					"name":            f.Name,
					"status":          f.Status,
					"size":            f.Size,
					"error":           f.Error,
					"itemId":          f.ItemID,
					"media":           f.Media,
					"ref_user_msg_id": userMsgID,
				}
				filesData = append(filesData, fileMap)
			}
			body["files"] = filesData
			body["current_user_message_id"] = userMsgID
		}
	}

	bodyBytes, _ := json.Marshal(body)
	LogDebug("[UpstreamBody] %s", string(bodyBytes))

	req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, "", err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-FE-Version", GetFeVersion())
	req.Header.Set("X-Signature", signature)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Origin", "https://chat.z.ai")
	req.Header.Set("Referer", fmt.Sprintf("https://chat.z.ai/c/%s", uuid.New().String()))
	req.Header.Set("User-Agent", uarand.GetRandom())

	// LogDebug("[Request] URL: %s", url)
	// LogDebug("[Request] Headers: %v", req.Header)

	client := getHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}

	return resp, targetModel, nil
}

type UpstreamData struct {
	Type string `json:"type"`
	Data struct {
		DeltaContent string `json:"delta_content"`
		EditContent  string `json:"edit_content"`
		Phase        string `json:"phase"`
		Done         bool   `json:"done"`
	} `json:"data"`
}

// 思考内容过滤器状态
type ThinkingFilter struct {
	hasSeenFirstThinking bool
	buffer               string
}

// 处理思考阶段的内容
// 第一个 delta_content 包含 <details...>\n<summary>Thinking…</summary>\n> 前缀，需要过滤
// 后续 delta_content 需要替换 "\n> " 为 "\n"（跨块累积处理）
func (f *ThinkingFilter) ProcessThinking(deltaContent string) string {
	if !f.hasSeenFirstThinking {
		f.hasSeenFirstThinking = true
		// 第一个 thinking 内容，查找 "> " 之后的内容
		if idx := strings.Index(deltaContent, "> "); idx != -1 {
			deltaContent = deltaContent[idx+2:]
		} else {
			return ""
		}
	}

	// 合并缓冲区内容
	content := f.buffer + deltaContent
	f.buffer = ""

	// 替换完整的 "\n> " 为 "\n"
	content = strings.ReplaceAll(content, "\n> ", "\n")

	// 检查末尾是否有可能是 "\n> " 的前缀
	// 可能的前缀："\n", "\n>"
	if strings.HasSuffix(content, "\n>") {
		f.buffer = "\n>"
		return content[:len(content)-2]
	}
	if strings.HasSuffix(content, "\n") {
		f.buffer = "\n"
		return content[:len(content)-1]
	}

	return content
}

// Flush 返回缓冲区中剩余的内容
func (f *ThinkingFilter) Flush() string {
	result := f.buffer
	f.buffer = ""
	return result
}

// 从 answer 阶段的 edit_content 中提取完整思考内容
// 格式：true" duration="0" ...>\n<summary>Thought for 0 seconds</summary>\n> 完整思考内容\n</details>\n你好
func (f *ThinkingFilter) ExtractCompleteThinking(editContent string) string {
	// 查找 "> " 到 "</details>" 之间的内容
	startIdx := strings.Index(editContent, "> ")
	if startIdx == -1 {
		return ""
	}
	startIdx += 2

	endIdx := strings.Index(editContent, "\n</details>")
	if endIdx == -1 {
		return ""
	}

	content := editContent[startIdx:endIdx]
	// 替换 "\n> " 为 "\n"
	content = strings.ReplaceAll(content, "\n> ", "\n")
	return content
}

// toolCallTracker 累积单个工具调用的流式增量
type toolCallTracker struct {
	id        string
	name      string
	arguments string
	nameSent  bool
	index     int
}

func (t *toolCallTracker) hasContent() bool {
	return t.name != "" || t.arguments != ""
}

func (t *toolCallTracker) toToolCall() ToolCall {
	return ToolCall{
		Index:    t.index,
		ID:       t.id,
		Type:     "function",
		Function: FunctionCall{Name: t.name, Arguments: t.arguments},
	}
}

func (t *toolCallTracker) toDelta() ToolCall {
	tc := t.toToolCall()
	if t.nameSent {
		tc.Function.Name = ""
	}
	return tc
}

func isJSONStart(s string) bool {
	return len(s) > 0 && (s[0] == '{' || s[0] == '[')
}

// functionCallStreamFilter 流式检测 <function_call> JSON 块
type functionCallStreamFilter struct {
	buffer string
	inCall bool
}

func (f *functionCallStreamFilter) Process(chunk string) (text string, calls []ToolCall) {
	f.buffer += chunk

	startTag := "<function_call>"
	endTag := "</function_call>"

	var textParts []string
	remaining := f.buffer

	for {
		if !f.inCall {
			idx := strings.Index(remaining, startTag)
			if idx == -1 {
				safe, hold := splitSafeForPartialTag(remaining, startTag)
				textParts = append(textParts, safe)
				remaining = hold
				break
			}
			textParts = append(textParts, remaining[:idx])
			remaining = remaining[idx+len(startTag):]
			f.inCall = true
		}

		if f.inCall {
			idx := strings.Index(remaining, endTag)
			if idx == -1 {
				break
			}
			jsonStr := strings.TrimSpace(remaining[:idx])
			remaining = remaining[idx+len(endTag):]
			f.inCall = false

			var parsed struct {
				Name      string      `json:"name"`
				Arguments interface{} `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(jsonStr), &parsed); err == nil {
				argsJSON, _ := json.Marshal(parsed.Arguments)
				calls = append(calls, ToolCall{
					Index:    len(calls),
					ID:       fmt.Sprintf("call_%s", uuid.New().String()[:12]),
					Type:     "function",
					Function: FunctionCall{Name: parsed.Name, Arguments: string(argsJSON)},
				})
			}
		}
	}

	f.buffer = remaining
	return strings.Join(textParts, ""), calls
}

func (f *functionCallStreamFilter) Flush() string {
	result := f.buffer
	f.buffer = ""
	f.inCall = false
	return result
}

func splitSafeForPartialTag(s, tag string) (safe, hold string) {
	for i := 1; i < len(tag) && i <= len(s); i++ {
		if strings.HasSuffix(s, tag[:i]) {
			return s[:len(s)-i], s[len(s)-i:]
		}
	}
	return s, ""
}

// parseFunctionCallsFromText 从文本中提取 <function_call> 块
func parseFunctionCallsFromText(text string) []ToolCall {
	var calls []ToolCall
	startTag := "<function_call>"
	endTag := "</function_call>"

	for {
		start := strings.Index(text, startTag)
		if start == -1 {
			break
		}
		end := strings.Index(text, endTag)
		if end == -1 {
			break
		}
		jsonStr := strings.TrimSpace(text[start+len(startTag) : end])
		text = text[end+len(endTag):]

		var parsed struct {
			Name      string      `json:"name"`
			Arguments interface{} `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
			continue
		}
		argsJSON, _ := json.Marshal(parsed.Arguments)
		calls = append(calls, ToolCall{
			Index:    len(calls),
			ID:       fmt.Sprintf("call_%s", uuid.New().String()[:12]),
			Type:     "function",
			Function: FunctionCall{Name: parsed.Name, Arguments: string(argsJSON)},
		})
	}
	return calls
}

func HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Model == "" {
		req.Model = "GLM-4.6"
	}

	resp, modelName, err := makeUpstreamRequest(token, req.Messages, req.Model, req.Tools, req.ToolChoice)
	if err != nil {
		LogError("Upstream request failed: %v", err)
		http.Error(w, "Upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if len(bodyStr) > 500 {
			bodyStr = bodyStr[:500]
		}
		LogError("Upstream error: status=%d, body=%s", resp.StatusCode, bodyStr)
		http.Error(w, "Upstream error", resp.StatusCode)
		return
	}

	completionID := fmt.Sprintf("chatcmpl-%s", uuid.New().String()[:29])

	if req.Stream {
		handleStreamResponse(w, resp.Body, completionID, modelName)
	} else {
		handleNonStreamResponse(w, resp.Body, completionID, modelName)
	}
}

func handleStreamResponse(w http.ResponseWriter, body io.ReadCloser, completionID, modelName string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	hasContent := false
	searchRefFilter := NewSearchRefFilter()
	thinkingFilter := &ThinkingFilter{}
	funcCallFilter := &functionCallStreamFilter{}
	pendingSourcesMarkdown := ""
	var toolCallTrackers []*toolCallTracker
	var currentToolTracker *toolCallTracker
	var textToolCalls []ToolCall

	for scanner.Scan() {
		line := scanner.Text()
		LogDebug("[Upstream] %s", line)

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var upstream UpstreamData
		if err := json.Unmarshal([]byte(payload), &upstream); err != nil {
			continue
		}

		if upstream.Data.Phase == "done" {
			break
		}

		// 处理思考阶段的增量内容
		if upstream.Data.Phase == "thinking" && upstream.Data.DeltaContent != "" {
			// 如果有待输出的搜索结果，先输出到 reasoning
			if pendingSourcesMarkdown != "" {
				hasContent = true
				chunk := ChatCompletionChunk{
					ID:      completionID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   modelName,
					Choices: []Choice{{
						Index:        0,
						Delta:        Delta{ReasoningContent: pendingSourcesMarkdown},
						FinishReason: nil,
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				pendingSourcesMarkdown = ""
			}

			reasoningContent := thinkingFilter.ProcessThinking(upstream.Data.DeltaContent)
			reasoningContent = searchRefFilter.Process(reasoningContent)
			if reasoningContent != "" {
				hasContent = true
				chunk := ChatCompletionChunk{
					ID:      completionID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   modelName,
					Choices: []Choice{{
						Index:        0,
						Delta:        Delta{ReasoningContent: reasoningContent},
						FinishReason: nil,
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
			continue
		}

		// 解析搜索结果，暂存等待下一个流决定放在哪里
		if upstream.Data.EditContent != "" && IsSearchResultContent(upstream.Data.EditContent) {
			if results := ParseSearchResults(upstream.Data.EditContent); len(results) > 0 {
				searchRefFilter.AddSearchResults(results)
				pendingSourcesMarkdown = searchRefFilter.GetSearchResultsMarkdown()
			}
			continue
		}
		// 跳过搜索工具调用
		if upstream.Data.EditContent != "" && IsSearchToolCall(upstream.Data.EditContent, upstream.Data.Phase) {
			continue
		}

		// 处理函数调用（tool_call + delta_content，非搜索类）
		if upstream.Data.Phase == "tool_call" && upstream.Data.DeltaContent != "" {
			deltaContent := upstream.Data.DeltaContent
			if currentToolTracker == nil || !isJSONStart(deltaContent) {
				// 新的工具调用
				if currentToolTracker != nil && currentToolTracker.hasContent() {
					toolCallTrackers = append(toolCallTrackers, currentToolTracker)
				}
				currentToolTracker = &toolCallTracker{
					id:    fmt.Sprintf("call_%s", uuid.New().String()[:12]),
					name:  strings.TrimSpace(deltaContent),
					index: len(toolCallTrackers),
				}
			}
			if currentToolTracker != nil {
				if isJSONStart(deltaContent) {
					currentToolTracker.arguments += deltaContent
				}
				hasContent = true
				tc := currentToolTracker.toDelta()
				currentToolTracker.nameSent = true
				chunk := ChatCompletionChunk{
					ID:      completionID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   modelName,
					Choices: []Choice{{
						Index:        0,
						Delta:        Delta{ToolCalls: []ToolCall{tc}},
						FinishReason: nil,
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
			continue
		}

		// 进入 answer 阶段，如果有待输出的搜索结果，先输出到 content
		if pendingSourcesMarkdown != "" {
			hasContent = true
			chunk := ChatCompletionChunk{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []Choice{{
					Index:        0,
					Delta:        Delta{Content: pendingSourcesMarkdown},
					FinishReason: nil,
				}},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			pendingSourcesMarkdown = ""
		}

		content := ""
		reasoningContent := ""

		// 先输出 thinking 缓冲区剩余内容
		if thinkingRemaining := thinkingFilter.Flush(); thinkingRemaining != "" {
			thinkingRemaining = searchRefFilter.Process(thinkingRemaining) + searchRefFilter.Flush()
			if thinkingRemaining != "" {
				hasContent = true
				chunk := ChatCompletionChunk{
					ID:      completionID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   modelName,
					Choices: []Choice{{
						Index:        0,
						Delta:        Delta{ReasoningContent: thinkingRemaining},
						FinishReason: nil,
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}

		if upstream.Data.Phase == "answer" && upstream.Data.DeltaContent != "" {
			content = upstream.Data.DeltaContent
		} else if upstream.Data.Phase == "answer" && upstream.Data.EditContent != "" {
			// 思考模型首次 answer：提取完整思考内容 + 正常回复开头
			if strings.Contains(upstream.Data.EditContent, "</details>") {
				reasoningContent = thinkingFilter.ExtractCompleteThinking(upstream.Data.EditContent)
				if idx := strings.Index(upstream.Data.EditContent, "</details>\n"); idx != -1 {
					content = upstream.Data.EditContent[idx+len("</details>\n"):]
				}
			}
		} else if (upstream.Data.Phase == "other" || upstream.Data.Phase == "tool_call") && upstream.Data.EditContent != "" {
			// other: 普通最后一个 token; tool_call: 搜索模式最后一个 token
			content = upstream.Data.EditContent
		}

		// 输出完整思考内容（如果有）
		if reasoningContent != "" {
			reasoningContent = searchRefFilter.Process(reasoningContent) + searchRefFilter.Flush()
		}
		if reasoningContent != "" {
			hasContent = true
			chunk := ChatCompletionChunk{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []Choice{{
					Index:        0,
					Delta:        Delta{ReasoningContent: reasoningContent},
					FinishReason: nil,
				}},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		if content != "" {
			// 流式检测 <function_call> JSON 块
			displayContent, newCalls := funcCallFilter.Process(content)
			for _, tc := range newCalls {
				hasContent = true
				chunk := ChatCompletionChunk{
					ID:      completionID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   modelName,
					Choices: []Choice{{
						Index:        0,
						Delta:        Delta{ToolCalls: []ToolCall{tc}},
						FinishReason: nil,
					}},
				}
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
			textToolCalls = append(textToolCalls, newCalls...)
			content = displayContent
		}

		if content == "" {
			continue
		}

		// 过滤搜索引用标记（跨流累积处理）
		content = searchRefFilter.Process(content)
		if content == "" {
			continue
		}

		hasContent = true
		chunk := ChatCompletionChunk{
			ID:      completionID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   modelName,
			Choices: []Choice{{
				Index:        0,
				Delta:        Delta{Content: content},
				FinishReason: nil,
			}},
		}

		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	if err := scanner.Err(); err != nil {
		LogError("[Upstream] scanner error: %v", err)
	}

	// Flush function call filter
	if remaining := funcCallFilter.Flush(); remaining != "" {
		remaining = searchRefFilter.Process(remaining) + searchRefFilter.Flush()
		if remaining != "" {
			hasContent = true
			chunk := ChatCompletionChunk{
				ID:      completionID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   modelName,
				Choices: []Choice{{
					Index:        0,
					Delta:        Delta{Content: remaining},
					FinishReason: nil,
				}},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}

	// 输出过滤器中剩余的内容（非引用标记的部分）
	if remaining := searchRefFilter.Flush(); remaining != "" {
		hasContent = true
		chunk := ChatCompletionChunk{
			ID:      completionID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   modelName,
			Choices: []Choice{{
				Index:        0,
				Delta:        Delta{Content: remaining},
				FinishReason: nil,
			}},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	if !hasContent && len(textToolCalls) == 0 {
		LogError("Stream response 200 but no content received")
	}

	// 最终化当前工具调用
	if currentToolTracker != nil && currentToolTracker.hasContent() {
		toolCallTrackers = append(toolCallTrackers, currentToolTracker)
	}

	// 合并 tool_call phase 的工具调用和文本中的函数调用
	allToolCalls := textToolCalls
	for _, t := range toolCallTrackers {
		allToolCalls = append(allToolCalls, t.toToolCall())
	}

	// 如果有工具调用，发送最终的 tool_calls 块
	if len(allToolCalls) > 0 {
		toolCallReason := "tool_calls"
		finalChunk := ChatCompletionChunk{
			ID:      completionID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   modelName,
			Choices: []Choice{{
				Index:        0,
				Delta:        Delta{ToolCalls: allToolCalls},
				FinishReason: &toolCallReason,
			}},
		}
		data, _ := json.Marshal(finalChunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// Final chunk
	stopReason := "stop"
	finalChunk := ChatCompletionChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []Choice{{
			Index:        0,
			Delta:        Delta{},
			FinishReason: &stopReason,
		}},
	}

	data, _ := json.Marshal(finalChunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func handleNonStreamResponse(w http.ResponseWriter, body io.ReadCloser, completionID, modelName string) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var chunks []string
	var reasoningChunks []string
	thinkingFilter := &ThinkingFilter{}
	searchRefFilter := NewSearchRefFilter()
	hasThinking := false
	pendingSourcesMarkdown := ""
	var toolCallTrackers []*toolCallTracker
	var currentToolTracker *toolCallTracker

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var upstream UpstreamData
		if err := json.Unmarshal([]byte(payload), &upstream); err != nil {
			continue
		}

		if upstream.Data.Phase == "done" {
			break
		}

		if upstream.Data.Phase == "thinking" && upstream.Data.DeltaContent != "" {
			if pendingSourcesMarkdown != "" {
				reasoningChunks = append(reasoningChunks, pendingSourcesMarkdown)
				pendingSourcesMarkdown = ""
			}
			hasThinking = true
			reasoningContent := thinkingFilter.ProcessThinking(upstream.Data.DeltaContent)
			if reasoningContent != "" {
				reasoningChunks = append(reasoningChunks, reasoningContent)
			}
			continue
		}

		if upstream.Data.EditContent != "" && IsSearchResultContent(upstream.Data.EditContent) {
			if results := ParseSearchResults(upstream.Data.EditContent); len(results) > 0 {
				searchRefFilter.AddSearchResults(results)
				pendingSourcesMarkdown = searchRefFilter.GetSearchResultsMarkdown()
			}
			continue
		}
		if upstream.Data.EditContent != "" && IsSearchToolCall(upstream.Data.EditContent, upstream.Data.Phase) {
			continue
		}

		// 进入 answer 阶段，把待输出的搜索结果放到 content
		if pendingSourcesMarkdown != "" && !hasThinking {
			chunks = append(chunks, pendingSourcesMarkdown)
			pendingSourcesMarkdown = ""
		}

		content := ""
		if upstream.Data.Phase == "answer" && upstream.Data.DeltaContent != "" {
			content = upstream.Data.DeltaContent
		} else if upstream.Data.Phase == "answer" && upstream.Data.EditContent != "" {
			if strings.Contains(upstream.Data.EditContent, "</details>") {
				reasoningContent := thinkingFilter.ExtractCompleteThinking(upstream.Data.EditContent)
				if reasoningContent != "" {
					reasoningChunks = append(reasoningChunks, reasoningContent)
				}
				if idx := strings.Index(upstream.Data.EditContent, "</details>\n"); idx != -1 {
					content = upstream.Data.EditContent[idx+len("</details>\n"):]
				}
			}
		} else if (upstream.Data.Phase == "other" || upstream.Data.Phase == "tool_call") && upstream.Data.EditContent != "" {
			content = upstream.Data.EditContent
		}

	if content != "" {
			chunks = append(chunks, content)
		}

		// 处理函数调用增量（tool_call + delta_content）
		if upstream.Data.Phase == "tool_call" && upstream.Data.DeltaContent != "" {
			deltaContent := upstream.Data.DeltaContent
			if currentToolTracker == nil || !isJSONStart(deltaContent) {
				if currentToolTracker != nil && currentToolTracker.hasContent() {
					toolCallTrackers = append(toolCallTrackers, currentToolTracker)
				}
				currentToolTracker = &toolCallTracker{
					id:    fmt.Sprintf("call_%s", uuid.New().String()[:12]),
					name:  strings.TrimSpace(deltaContent),
					index: len(toolCallTrackers),
				}
			}
			if currentToolTracker != nil && isJSONStart(deltaContent) {
				currentToolTracker.arguments += deltaContent
			}
			if currentToolTracker != nil {
				currentToolTracker.nameSent = true
			}
		}
	}

	if currentToolTracker != nil && currentToolTracker.hasContent() {
		toolCallTrackers = append(toolCallTrackers, currentToolTracker)
	}

	fullContent := strings.Join(chunks, "")
	fullContent = searchRefFilter.Process(fullContent) + searchRefFilter.Flush()
	fullReasoning := strings.Join(reasoningChunks, "")
	fullReasoning = searchRefFilter.Process(fullReasoning) + searchRefFilter.Flush()

	// 从文本中提取函数调用
	var textToolCalls []ToolCall
	if fullContent != "" {
		textToolCalls = parseFunctionCallsFromText(fullContent)
		if len(textToolCalls) > 0 {
			// 移除 function_call 标签，保留纯净内容
			re := regexp.MustCompile(`<function_call>[\s\S]*?</function_call>`)
			fullContent = strings.TrimSpace(re.ReplaceAllString(fullContent, ""))
		}
	}

	if fullContent == "" && len(toolCallTrackers) == 0 && len(textToolCalls) == 0 {
		LogError("Non-stream response 200 but no content received")
	}

	finishReason := "stop"
	var toolCalls []ToolCall
	// 合并 tool_call phase 的工具调用和文本中的函数调用
	toolCalls = append(toolCalls, textToolCalls...)
	for _, t := range toolCallTrackers {
		toolCalls = append(toolCalls, t.toToolCall())
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	response := ChatCompletionResponse{
		ID:      completionID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []Choice{{
			Index: 0,
			Message: &MessageResp{
				Role:             "assistant",
				Content:          fullContent,
				ReasoningContent: fullReasoning,
				ToolCalls:        toolCalls,
			},
			FinishReason: &finishReason,
		}},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func HandleModels(w http.ResponseWriter, r *http.Request) {
	var models []ModelInfo
	for _, id := range ModelList {
		models = append(models, ModelInfo{
			ID:      id,
			Object:  "model",
			OwnedBy: "z.ai",
		})
	}

	response := ModelsResponse{
		Object: "list",
		Data:   models,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
