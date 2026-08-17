package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAgentPromptBudgetRejectsBeforeServiceAccess(t *testing.T) {
	if err := validateAgentPrompt(strings.Repeat("x", maxAgentPromptBytes)); err != nil {
		t.Fatalf("exact-limit prompt rejected: %v", err)
	}
	service := &Service{}
	if _, err := service.Start(strings.Repeat("x", maxAgentPromptBytes+1)); !errors.Is(err, errAgentPromptTooLarge) {
		t.Fatalf("oversize Start error = %v, want errAgentPromptTooLarge", err)
	}
}

func TestCompletionPayloadBudgets(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		exactContentBytes := maxAgentMessageBytes - len("assistant")
		if err := validateCompletionPayload(completion{Content: strings.Repeat("x", exactContentBytes)}); err != nil {
			t.Fatalf("exact-limit content rejected: %v", err)
		}
		err := validateCompletionPayload(completion{Content: strings.Repeat("x", exactContentBytes+1)})
		if !errors.Is(err, errAgentMessageTooLarge) {
			t.Fatalf("oversize content error = %v, want errAgentMessageTooLarge", err)
		}
	})

	t.Run("tool arguments", func(t *testing.T) {
		call := wireToolCall{Type: "function", Function: wireFunc{Name: "read_file", Arguments: strings.Repeat("x", maxAgentToolArgumentsBytes+1)}}
		err := validateCompletionPayload(completion{ToolCalls: []wireToolCall{call}})
		if err == nil || !strings.Contains(err.Error(), "arguments") {
			t.Fatalf("oversize tool arguments error = %v", err)
		}
	})

	t.Run("tool count", func(t *testing.T) {
		calls := make([]wireToolCall, maxAgentToolCallsPerMessage+1)
		err := validateCompletionPayload(completion{ToolCalls: calls})
		if err == nil || !strings.Contains(err.Error(), "tool calls") {
			t.Fatalf("oversize tool count error = %v", err)
		}
	})

	t.Run("combined content and tool calls", func(t *testing.T) {
		call := wireToolCall{Type: "function", Function: wireFunc{Name: "read_file", Arguments: strings.Repeat("x", maxAgentToolArgumentsBytes)}}
		contentBytes := maxAgentMessageBytes - int(wireMessagePayloadBytes(wireMsg{Role: "assistant", ToolCalls: []wireToolCall{call}})) + 1
		err := validateCompletionPayload(completion{Content: strings.Repeat("x", contentBytes), ToolCalls: []wireToolCall{call}})
		if !errors.Is(err, errAgentMessageTooLarge) {
			t.Fatalf("combined oversize completion error = %v, want errAgentMessageTooLarge", err)
		}
	})
}

func TestCompleteRejectsProviderToolArgumentsBeforeDispatch(t *testing.T) {
	response := `{"choices":[{"message":{"tool_calls":[{"id":"call","type":"function","function":{"name":"read_file","arguments":"` + strings.Repeat("x", maxAgentToolArgumentsBytes+1) + `"}}]}}]}`
	server := completionTestServer(t, response)
	defer server.Close()
	service := &Service{http: server.Client()}
	_, err := service.complete(context.Background(), "run-budget", server.URL, "model", "", []wireMsg{{Role: "user", Content: "hello"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("complete error = %v, want tool-argument budget rejection", err)
	}
}

func TestConversationAndRunTranscriptsAreByteBounded(t *testing.T) {
	chunk := strings.Repeat("x", maxAgentMessageBytes-64)
	messages := make([]wireMsg, 0, 24)
	for i := 0; i < 24; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, wireMsg{Role: role, Content: chunk})
	}
	trimmed := trimConversationMessages(messages)
	if len(trimmed) >= len(messages) {
		t.Fatalf("byte trimming retained all %d messages", len(messages))
	}
	if got := transcriptPayloadBytes(trimmed); got > maxAgentTranscriptBytes {
		t.Fatalf("trimmed transcript = %d bytes, limit %d", got, maxAgentTranscriptBytes)
	}

	run := append([]wireMsg{{Role: "system", Content: "system"}}, messages...)
	run = trimRunMessages(run)
	if len(run) == 0 || run[0].Role != "system" {
		t.Fatalf("run trimming lost the system message: %+v", run)
	}
	if got := transcriptPayloadBytes(run); got > maxAgentTranscriptBytes {
		t.Fatalf("trimmed run transcript = %d bytes, limit %d", got, maxAgentTranscriptBytes)
	}
}

func TestConversationTrimDoesNotOrphanLeadingToolResult(t *testing.T) {
	large := strings.Repeat("x", maxAgentMessageBytes-32)
	messages := []wireMsg{{Role: "user", Content: large}}
	for i := 0; i < 20; i++ {
		messages = append(messages,
			wireMsg{Role: "assistant", ToolCalls: []wireToolCall{{ID: fmt.Sprintf("call-%d", i), Function: wireFunc{Name: "read_file", Arguments: `{}`}}}},
			wireMsg{Role: "tool", ToolCallID: fmt.Sprintf("call-%d", i), Content: large},
		)
	}
	trimmed := trimConversationMessages(messages)
	if len(trimmed) > 0 && trimmed[0].Role == "tool" {
		t.Fatalf("trimmed transcript begins with an orphan tool result")
	}
	if got := transcriptPayloadBytes(trimmed); got > maxAgentTranscriptBytes {
		t.Fatalf("trimmed transcript = %d bytes, limit %d", got, maxAgentTranscriptBytes)
	}
}

func TestToolSelectionTextIsBoundedAndKeepsNewestIntent(t *testing.T) {
	messages := []wireMsg{
		{Role: "user", Content: strings.Repeat("oldé", maxAgentSelectionBytes)},
		{Role: "assistant", Content: "newest intent: run command and inspect database"},
	}
	selection := toolSelectionText(messages)
	if len(selection) > maxAgentSelectionBytes {
		t.Fatalf("selection = %d bytes, limit %d", len(selection), maxAgentSelectionBytes)
	}
	if !utf8.ValidString(selection) {
		t.Fatal("selection truncation split a UTF-8 sequence")
	}
	if !strings.Contains(selection, "newest intent") {
		t.Fatalf("selection lost newest intent: %q", selection[len(selection)-min(len(selection), 100):])
	}
}

func TestBoundedToolCallHistoryCapsKeysAndDetectsRecentDuplicates(t *testing.T) {
	history := newBoundedToolCallHistory(8)
	if got := history.observe("read_file", `{"path":"current"}`); got != 1 {
		t.Fatalf("first observation = %d", got)
	}
	if got := history.observe("read-file", ` {"path":"current"} `); got != 2 {
		t.Fatalf("normalized duplicate observation = %d", got)
	}
	for i := 0; i < 100; i++ {
		history.observe("read_file", fmt.Sprintf(`{"path":"%d"}`, i))
	}
	if len(history.counts) != 8 || len(history.order) != 8 {
		t.Fatalf("bounded history sizes = counts %d order %d, want 8/8", len(history.counts), len(history.order))
	}
	if got := history.observe("read_file", `{"path":"99"}`); got != 2 {
		t.Fatalf("recent duplicate observation = %d", got)
	}
}

func transcriptPayloadBytes(messages []wireMsg) int64 {
	var total int64
	for _, message := range messages {
		total = saturatingAdd(total, wireMessagePayloadBytes(message))
	}
	return total
}
