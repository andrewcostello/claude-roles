package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var deepseekAPIURL = "https://api.deepseek.com/v1/chat/completions"

func main() {
	modelFlag := flag.String("model", "deepseek-v4-flash", "DeepSeek model (deepseek-v4-flash or deepseek-v4-pro)")
	jsonSchemaFlag := flag.String("json-schema", "", "JSON schema string the output must conform to (embedded in system prompt)")
	systemPromptFlag := flag.String("system-prompt", "", "System prompt text (appended to default review system prompt)")
	cwdFlag := flag.String("cwd", ".", "Working directory for file tools")
	maxTurnsFlag := flag.Int("max-turns", 25, "Maximum tool-use turns before forced output")
	timeoutFlag := flag.Duration("timeout", 12*time.Minute, "Total timeout for the tool-use loop")
	temperatureFlag := flag.Float64("temperature", 0.1, "Temperature for investigation turns (0.0-2.0). Final JSON turn always uses 0.0.")
	flag.Parse()

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		log.Fatal("DEEPSEEK_API_KEY environment variable not set")
	}

	promptBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("failed to read prompt from stdin: %v", err)
	}
	prompt := string(promptBytes)
	if strings.TrimSpace(prompt) == "" {
		log.Fatal("empty prompt on stdin")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	result, err := runToolLoop(ctx, apiKey, *modelFlag, *jsonSchemaFlag, *systemPromptFlag, *cwdFlag, prompt, *maxTurnsFlag, *temperatureFlag)
	if err != nil {
		log.Fatalf("deepseek tool loop failed: %v", err)
	}

	os.Stdout.WriteString(result)
}

// --- API types ----------------------------------------------------------------

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Tools          []toolDef       `json:"tools,omitempty"`
	ToolChoice     string          `json:"tool_choice,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    float64         `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolDef struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role             string     `json:"role"`
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []toolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// --- tool definitions ---------------------------------------------------------

func readOnlyTools() []toolDef {
	return []toolDef{
		{
			Type: "function",
			Function: functionDef{
				Name:        "read_file",
				Description: "Read the full contents of a file at the given path relative to the working directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "File path relative to the working directory.",
						},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: functionDef{
				Name:        "grep",
				Description: "Search for a pattern in files under the working directory using ripgrep. Returns matching lines with file:line prefix.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{
							"type":        "string",
							"description": "Pattern to search for (regex syntax).",
						},
						"glob": map[string]any{
							"type":        "string",
							"description": "Optional file glob filter (e.g. '*.go').",
						},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			Type: "function",
			Function: functionDef{
				Name:        "list_dir",
				Description: "List files and directories at the given path relative to the working directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Directory path relative to the working directory. Defaults to root.",
						},
					},
				},
			},
		},
	}
}

// --- tool execution -----------------------------------------------------------

type toolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	Error      string `json:"error,omitempty"`
}

func executeTool(cwd string, call toolCall) toolResult {
	result := toolResult{ToolCallID: call.ID}

	switch call.Function.Name {
	case "read_file":
		result.Content, result.Error = toolReadFile(cwd, call.Function.Arguments)
	case "grep":
		result.Content, result.Error = toolGrep(cwd, call.Function.Arguments)
	case "list_dir":
		result.Content, result.Error = toolListDir(cwd, call.Function.Arguments)
	default:
		result.Error = fmt.Sprintf("unknown tool: %s", call.Function.Name)
	}
	return result
}

func toolReadFile(cwd, rawArgs string) (string, string) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "", fmt.Sprintf("invalid arguments for read_file: %v", err)
	}
	if args.Path == "" {
		return "", "read_file: path is required"
	}

	fullPath := filepath.Join(cwd, args.Path)
	// Prevent traversal outside cwd
	resolved, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Sprintf("failed to resolve path: %v", err)
	}
	cwdAbs, _ := filepath.Abs(cwd)
	if !strings.HasPrefix(resolved, cwdAbs+string(filepath.Separator)) && resolved != cwdAbs {
		return "", fmt.Sprintf("path %q is outside working directory", args.Path)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Sprintf("failed to read file %q: %v", args.Path, err)
	}

	// Truncate to prevent blowing out context
	const maxBytes = 512 * 1024 // 512KB
	if len(data) > maxBytes {
		return string(data[:maxBytes]) + fmt.Sprintf("\n\n[TRUNCATED: file is %d bytes, showing first %d]", len(data), maxBytes), ""
	}
	return string(data), ""
}

func toolGrep(cwd, rawArgs string) (string, string) {
	var args struct {
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "", fmt.Sprintf("invalid arguments for grep: %v", err)
	}
	if args.Pattern == "" {
		return "", "grep: pattern is required"
	}

	rgArgs := []string{"-n", "--glob", "!.git", "--glob", "!vendor", "--glob", "!node_modules"}
	if args.Glob != "" {
		rgArgs = append(rgArgs, "--glob", args.Glob)
	}
	rgArgs = append(rgArgs, args.Pattern, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "rg", rgArgs...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", "grep timed out after 10s"
		}
		if outBuf.Len() == 0 {
			return fmt.Sprintf("no matches found for %q", args.Pattern), ""
		}
	}

	output := outBuf.String()
	const maxLines = 100
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > maxLines {
		output = strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n\n[TRUNCATED: %d total matches, showing first %d]", len(lines), maxLines)
	}
	if output == "" {
		output = fmt.Sprintf("no matches found for %q", args.Pattern)
	}
	return output, ""
}

func toolListDir(cwd, rawArgs string) (string, string) {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal([]byte(rawArgs), &args) // optional, ignore parse errors

	dirPath := cwd
	if args.Path != "" {
		dirPath = filepath.Join(cwd, args.Path)
	}

	// Prevent traversal outside cwd
	resolved, err := filepath.Abs(dirPath)
	if err != nil {
		return "", fmt.Sprintf("failed to resolve path: %v", err)
	}
	cwdAbs, _ := filepath.Abs(cwd)
	if !strings.HasPrefix(resolved, cwdAbs+string(filepath.Separator)) && resolved != cwdAbs {
		return "", fmt.Sprintf("path %q is outside working directory", args.Path)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "", fmt.Sprintf("failed to list directory %q: %v", args.Path, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Contents of %s:\n", resolved)
	for _, e := range entries {
		info, err := e.Info()
		size := ""
		if err == nil {
			size = fmt.Sprintf(" %d bytes", info.Size())
		}
		typ := "file"
		if e.IsDir() {
			typ = "dir"
		}
		fmt.Fprintf(&b, "  [%s] %s%s\n", typ, e.Name(), size)
	}

	const maxEntries = 200
	if len(entries) > maxEntries {
		b.WriteString(fmt.Sprintf("\n[TRUNCATED: %d total entries, showing first %d]", len(entries), maxEntries))
	}
	return b.String(), ""
}

// --- HTTP client --------------------------------------------------------------

func callDeepSeekAPI(ctx context.Context, apiKey string, req chatRequest) (*chatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", deepseekAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse API response: %w\nBody: %s", err, string(respBody))
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("API error: %s (%s)", chatResp.Error.Message, chatResp.Error.Type)
	}

	return &chatResp, nil
}

// --- tool-use loop ------------------------------------------------------------

func runToolLoop(ctx context.Context, apiKey, model, jsonSchema, systemPrompt, cwd, userPrompt string, maxTurns int, temperature float64) (string, error) {
	messages := []chatMessage{}

	// Build system message
	var systemParts []string
	systemParts = append(systemParts, "You are a focused code reviewer. Use the provided tools to read files and search the codebase. After your investigation, produce your final answer as a raw JSON object.")

	if jsonSchema != "" {
		systemParts = append(systemParts, fmt.Sprintf(
			"Your FINAL output must be ONLY a raw JSON object conforming to this schema:\n%s\n"+
				"Do NOT wrap the JSON in markdown fences. Every field marked as required MUST be present. Do not add extra fields beyond the schema.",
			jsonSchema,
		))
	}

	if systemPrompt != "" {
		systemParts = append(systemParts, systemPrompt)
	}

	messages = append(messages, chatMessage{Role: "system", Content: strings.Join(systemParts, "\n\n")})
	messages = append(messages, chatMessage{Role: "user", Content: userPrompt})

	tools := readOnlyTools()

	// Phase 1: Investigation — tool-use loop
	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timeout after %d investigation turns", turn)
		}

		req := chatRequest{
			Model:       model,
			Messages:    messages,
			Tools:       tools,
			ToolChoice:  "auto",
			MaxTokens:   16384,
			Temperature: temperature,
		}

		resp, err := callDeepSeekAPI(ctx, apiKey, req)
		if err != nil {
			return "", fmt.Errorf("turn %d: %w", turn, err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("turn %d: API returned no choices", turn)
		}

		choice := resp.Choices[0]
		msg := choice.Message

		if len(msg.ToolCalls) > 0 {
			assistantMsg := chatMessage{Role: "assistant", ToolCalls: msg.ToolCalls}
			messages = append(messages, assistantMsg)

			for _, tc := range msg.ToolCalls {
				log.Printf("turn %d: executing tool %s(%s)", turn, tc.Function.Name, tc.Function.Arguments)
				result := executeTool(cwd, tc)

				var toolContent string
				if result.Error != "" {
					toolContent = fmt.Sprintf("ERROR: %s", result.Error)
				} else {
					toolContent = result.Content
				}

				messages = append(messages, chatMessage{
					Role:       "tool",
					ToolCallID: result.ToolCallID,
					Content:    toolContent,
				})
			}
			continue
		}

		// No tool calls — model produced final content
		content := msg.Content
		if content != "" {
			content = normalizeJSON(content)
			if json.Valid([]byte(content)) {
				return content, nil
			}
		}
		// Content is empty or not valid JSON — fall through to Phase 2
		break
	}

	// Phase 2: Force JSON output — one final turn without tools.
	// The model has all investigation results in the conversation history.
	// Strip tools so it MUST produce text, and response_format: json_object
	// ensures that text is valid JSON.
	log.Printf("phase 2: forcing final JSON output (no tools)")
	messages = append(messages, chatMessage{
		Role:    "user",
		Content: "You have completed your investigation. Now produce your final output as ONLY a raw JSON object matching the schema. No tools are available — output JSON directly.",
	})

	req := chatRequest{
		Model:          model,
		Messages:       messages,
		ResponseFormat: &responseFormat{Type: "json_object"},
		MaxTokens:      16384,
		Temperature:    0.0,
	}

	resp, err := callDeepSeekAPI(ctx, apiKey, req)
	if err != nil {
		return "", fmt.Errorf("final JSON turn failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("final turn: API returned no choices")
	}

	content := resp.Choices[0].Message.Content
	if content == "" {
		return "", fmt.Errorf("final turn: model returned empty content")
	}

	content = normalizeJSON(content)
	if !json.Valid([]byte(content)) {
		return "", fmt.Errorf("final turn: output is not valid JSON (starts with: %.80q)", truncate(content, 80))
	}

	return content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// normalizeJSON strips markdown fences from model output, matching the pattern
// in cmd/reviewer/main.go normalizeProviderJSON.
func normalizeJSON(raw string) string {
	s := strings.TrimSpace(raw)
	// Strip markdown fences
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
	}
	// Strip DeepSeek system markers (e.g. __DEEPCODE_PWD__<id>__<path>)
	if idx := strings.Index(s, "__DEEPCODE_PWD__"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
