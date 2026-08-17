package agent

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"strings"
	"unicode/utf8"
)

// Agent payload budgets are backend policy, not renderer hints. They cap both
// bridge-supplied input and provider-controlled completion data before either
// can enter retained transcript state.
const (
	maxAgentPromptBytes         = 256 * 1024
	maxAgentMessageBytes        = 1 * 1024 * 1024
	maxAgentToolArgumentsBytes  = 256 * 1024
	maxAgentToolCallNameBytes   = 256
	maxAgentToolCallIDBytes     = 4 * 1024
	maxAgentToolCallsPerMessage = 32
	maxAgentTranscriptBytes     = 16 * 1024 * 1024
	maxAgentSelectionBytes      = 64 * 1024
	maxAgentSeenSignatures      = 4096
)

var (
	errAgentPromptTooLarge     = errors.New("agent prompt is too large")
	errAgentMessageTooLarge    = errors.New("agent message is too large")
	errAgentTranscriptTooLarge = errors.New("agent transcript is too large")
)

func validateAgentPrompt(prompt string) error {
	if len(prompt) > maxAgentPromptBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d-byte limit", errAgentPromptTooLarge, len(prompt), maxAgentPromptBytes)
	}
	return nil
}

func validateCompletionPayload(comp completion) error {
	if len(comp.ToolCalls) > maxAgentToolCallsPerMessage {
		return fmt.Errorf("provider completion contains %d tool calls; maximum is %d", len(comp.ToolCalls), maxAgentToolCallsPerMessage)
	}
	for i, call := range comp.ToolCalls {
		if len(call.ID) > maxAgentToolCallIDBytes {
			return fmt.Errorf("provider tool call %d id exceeds the %d-byte limit", i+1, maxAgentToolCallIDBytes)
		}
		if len(call.Type) > maxAgentToolCallNameBytes {
			return fmt.Errorf("provider tool call %d type exceeds the %d-byte limit", i+1, maxAgentToolCallNameBytes)
		}
		if len(call.Function.Name) > maxAgentToolCallNameBytes {
			return fmt.Errorf("provider tool call %d name exceeds the %d-byte limit", i+1, maxAgentToolCallNameBytes)
		}
		if len(call.Function.Arguments) > maxAgentToolArgumentsBytes {
			return fmt.Errorf("provider tool call %d arguments exceed the %d-byte limit", i+1, maxAgentToolArgumentsBytes)
		}
	}
	// Content and tool calls arrive in one provider message even though run()
	// later stores them as separately paired messages. Enforce the aggregate at
	// this trust boundary so a response cannot consume two independent limits.
	message := wireMsg{Role: "assistant", Content: comp.Content, ToolCalls: comp.ToolCalls}
	if err := validateWireMessage(message); err != nil {
		return fmt.Errorf("provider completion message: %w", err)
	}
	return nil
}

func validateWireMessage(msg wireMsg) error {
	if wireMessagePayloadBytes(msg) > maxAgentMessageBytes {
		return fmt.Errorf("%w: payload exceeds the %d-byte limit", errAgentMessageTooLarge, maxAgentMessageBytes)
	}
	if len(msg.ToolCalls) > maxAgentToolCallsPerMessage {
		return fmt.Errorf("%w: %d tool calls exceeds the per-message limit of %d", errAgentMessageTooLarge, len(msg.ToolCalls), maxAgentToolCallsPerMessage)
	}
	for i, call := range msg.ToolCalls {
		if len(call.ID) > maxAgentToolCallIDBytes {
			return fmt.Errorf("%w: tool call %d id exceeds the %d-byte limit", errAgentMessageTooLarge, i+1, maxAgentToolCallIDBytes)
		}
		if len(call.Type) > maxAgentToolCallNameBytes || len(call.Function.Name) > maxAgentToolCallNameBytes {
			return fmt.Errorf("%w: tool call %d metadata exceeds the %d-byte limit", errAgentMessageTooLarge, i+1, maxAgentToolCallNameBytes)
		}
		if len(call.Function.Arguments) > maxAgentToolArgumentsBytes {
			return fmt.Errorf("%w: tool call %d arguments exceed the %d-byte limit", errAgentMessageTooLarge, i+1, maxAgentToolArgumentsBytes)
		}
	}
	return nil
}

func validateProviderMessages(msgs []wireMsg) error {
	if len(msgs) > maxConversationMessages {
		return fmt.Errorf("%w: %d messages exceeds the limit of %d", errAgentTranscriptTooLarge, len(msgs), maxConversationMessages)
	}
	var total int64
	for i, msg := range msgs {
		if err := validateWireMessage(msg); err != nil {
			return fmt.Errorf("provider message %d: %w", i+1, err)
		}
		total = saturatingAdd(total, wireMessagePayloadBytes(msg))
		if total > maxAgentTranscriptBytes {
			return fmt.Errorf("%w: provider message payload exceeds the %d-byte limit", errAgentTranscriptTooLarge, maxAgentTranscriptBytes)
		}
	}
	return nil
}

func wireMessagePayloadBytes(msg wireMsg) int64 {
	var total int64
	for _, field := range []string{msg.Role, msg.Content, msg.ToolCallID, msg.Name} {
		total = saturatingAdd(total, int64(len(field)))
	}
	for _, call := range msg.ToolCalls {
		for _, field := range []string{call.ID, call.Type, call.Function.Name, call.Function.Arguments} {
			total = saturatingAdd(total, int64(len(field)))
		}
	}
	return total
}

func saturatingAdd(total, next int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if next > 0 && total > maxInt64-next {
		return maxInt64
	}
	return total + next
}

func trimWireMessages(msgs []wireMsg, maxMessages int, maxBytes int64) []wireMsg {
	if len(msgs) == 0 || maxMessages <= 0 || maxBytes <= 0 {
		return nil
	}
	var total int64
	for _, msg := range msgs {
		total = saturatingAdd(total, wireMessagePayloadBytes(msg))
	}
	cut := 0
	for cut < len(msgs) && (len(msgs)-cut > maxMessages || total > maxBytes) {
		total -= wireMessagePayloadBytes(msgs[cut])
		cut++
	}
	// Never retain a leading tool result without its assistant tool-call message.
	for cut < len(msgs) && msgs[cut].Role == "tool" {
		total -= wireMessagePayloadBytes(msgs[cut])
		cut++
	}
	if cut == 0 {
		return msgs
	}
	out := make([]wireMsg, 0, len(msgs)-cut)
	for _, msg := range msgs[cut:] {
		out = append(out, cloneWireMsg(msg))
	}
	return out
}

func trimRunMessages(msgs []wireMsg) []wireMsg {
	if len(msgs) == 0 {
		return nil
	}
	head := 0
	var headBytes int64
	for head < len(msgs) && msgs[head].Role == "system" {
		headBytes = saturatingAdd(headBytes, wireMessagePayloadBytes(msgs[head]))
		head++
	}
	if headBytes > maxAgentTranscriptBytes {
		return nil
	}
	if headBytes == maxAgentTranscriptBytes || head >= maxConversationMessages {
		return trimWireMessages(msgs[:head], maxConversationMessages, maxAgentTranscriptBytes)
	}
	rest := trimWireMessages(msgs[head:], maxConversationMessages-head, maxAgentTranscriptBytes-headBytes)
	if head == 0 {
		return rest
	}
	out := make([]wireMsg, 0, head+len(rest))
	out = append(out, msgs[:head]...)
	out = append(out, rest...)
	return out
}

func appendRunMessages(msgs []wireMsg, additions ...wireMsg) []wireMsg {
	msgs = append(msgs, additions...)
	return trimRunMessages(msgs)
}

// boundedSelectionText keeps only the newest selection material and never
// allocates more than maxAgentSelectionBytes for its final string. Partial
// retention takes a UTF-8-safe suffix because recent text carries the current
// task intent better than an old prefix.
func boundedSelectionText(msgs []wireMsg) string {
	if len(msgs) == 0 {
		return ""
	}
	type part struct{ text string }
	parts := make([]part, 0, 64)
	total := 0
	add := func(value string) {
		if value == "" {
			return
		}
		parts = append(parts, part{text: value})
		total += len(value) + 1
		for total > maxAgentSelectionBytes && len(parts) > 0 {
			over := total - maxAgentSelectionBytes
			first := parts[0].text
			if over >= len(first)+1 {
				total -= len(first) + 1
				parts = parts[1:]
				continue
			}
			keep := len(first) - over
			first = utf8SafeSuffix(first, keep)
			total -= len(parts[0].text) - len(first)
			parts[0].text = first
			break
		}
	}
	start := max(0, len(msgs)-40)
	for _, msg := range msgs[start:] {
		switch msg.Role {
		case "user", "assistant":
			add(msg.Content)
			calls := msg.ToolCalls
			if len(calls) > maxAgentToolCallsPerMessage {
				calls = calls[len(calls)-maxAgentToolCallsPerMessage:]
			}
			for _, call := range calls {
				add(call.Function.Name)
			}
		case "tool":
			add(msg.Name)
		}
	}
	var b strings.Builder
	if total > 0 {
		b.Grow(total)
	}
	for _, item := range parts {
		b.WriteString(item.text)
		b.WriteByte('\n')
	}
	return b.String()
}

func utf8SafeSuffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

type boundedToolCallHistory struct {
	limit  int
	counts map[[sha256.Size]byte]uint8
	order  [][sha256.Size]byte
	next   int
}

func newBoundedToolCallHistory(limit int) *boundedToolCallHistory {
	if limit < 0 {
		limit = 0
	}
	return &boundedToolCallHistory{
		limit:  limit,
		counts: make(map[[sha256.Size]byte]uint8, limit),
		order:  make([][sha256.Size]byte, 0, limit),
	}
}

func (h *boundedToolCallHistory) observe(name, arguments string) int {
	if h == nil || h.limit == 0 {
		return 1
	}
	signature := toolCallDigest(name, arguments)
	if count := h.counts[signature]; count > 0 {
		if count < 2 {
			count++
			h.counts[signature] = count
		}
		return int(count)
	}
	if len(h.order) < h.limit {
		h.order = append(h.order, signature)
	} else {
		delete(h.counts, h.order[h.next])
		h.order[h.next] = signature
		h.next = (h.next + 1) % h.limit
	}
	h.counts[signature] = 1
	return 1
}

func toolCallDigest(name, arguments string) [sha256.Size]byte {
	h := sha256.New()
	writeHashString(h, normalizeToolName(name))
	_, _ = h.Write([]byte{0})
	writeHashString(h, strings.TrimSpace(arguments))
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func writeHashString(h hash.Hash, value string) {
	_, _ = h.Write([]byte(value))
}
