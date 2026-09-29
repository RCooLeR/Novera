package llm

import (
	"encoding/json"
	"fmt"
)

// validateSendRequest bounds bridge-controlled fields before the service reads
// settings or secrets. Count is checked first so a hostile large slice is
// rejected without walking or trimming every element.
func validateSendRequest(req SendRequest) error {
	if len(req.Messages) > maxSendMessages {
		return fmt.Errorf("%w: %d messages exceeds the limit of %d", ErrRequestTooLarge, len(req.Messages), maxSendMessages)
	}
	if len(req.System) > maxSystemBytes {
		return fmt.Errorf("%w: system prompt exceeds the %d-byte field limit", ErrRequestTooLarge, maxSystemBytes)
	}
	if len(req.Context) > maxContextBytes {
		return fmt.Errorf("%w: workspace context exceeds the %d-byte field limit", ErrRequestTooLarge, maxContextBytes)
	}

	var total int64
	add := func(field string, size int) error {
		total = saturatingByteAdd(total, int64(size))
		if total > maxChatRequestPayloadBytes {
			return fmt.Errorf("%w: aggregate fields exceed the %d-byte limit (at %s)", ErrRequestTooLarge, maxChatRequestPayloadBytes, field)
		}
		return nil
	}
	if err := add("system", len(req.System)); err != nil {
		return err
	}
	if err := add("context", len(req.Context)); err != nil {
		return err
	}
	for i, message := range req.Messages {
		if len(message.Role) > maxMessageRoleBytes {
			return fmt.Errorf("%w: message %d role exceeds the %d-byte field limit", ErrRequestTooLarge, i+1, maxMessageRoleBytes)
		}
		if len(message.Content) > maxMessageContentBytes {
			return fmt.Errorf("%w: message %d content exceeds the %d-byte field limit", ErrRequestTooLarge, i+1, maxMessageContentBytes)
		}
		if err := add(fmt.Sprintf("message %d role", i+1), len(message.Role)); err != nil {
			return err
		}
		if err := add(fmt.Sprintf("message %d content", i+1), len(message.Content)); err != nil {
			return err
		}
	}
	return nil
}

// validateChatRequest rechecks the normalized wire fields, including the model
// sourced from persisted settings and the fixed context prefix added by
// buildMessages. This is the final pre-marshal trust boundary.
func validateChatRequest(req chatRequest) error {
	if len(req.Model) > maxChatModelBytes {
		return fmt.Errorf("%w: model exceeds the %d-byte field limit", ErrRequestTooLarge, maxChatModelBytes)
	}
	if len(req.Messages) > maxSendMessages+2 {
		return fmt.Errorf("%w: normalized message count exceeds the limit of %d", ErrRequestTooLarge, maxSendMessages+2)
	}
	total := int64(len(req.Model))
	for i, message := range req.Messages {
		if len(message.Role) > maxMessageRoleBytes {
			return fmt.Errorf("%w: normalized message %d role exceeds the %d-byte field limit", ErrRequestTooLarge, i+1, maxMessageRoleBytes)
		}
		if len(message.Content) > maxMessageContentBytes {
			return fmt.Errorf("%w: normalized message %d content exceeds the %d-byte field limit", ErrRequestTooLarge, i+1, maxMessageContentBytes)
		}
		total = saturatingByteAdd(total, int64(len(message.Role)))
		total = saturatingByteAdd(total, int64(len(message.Content)))
		if total > maxChatRequestPayloadBytes {
			return fmt.Errorf("%w: normalized request payload exceeds the %d-byte aggregate limit", ErrRequestTooLarge, maxChatRequestPayloadBytes)
		}
	}
	return nil
}

func marshalChatRequest(req chatRequest) ([]byte, error) {
	if err := validateChatRequest(req); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxMarshaledChatRequestBytes {
		return nil, fmt.Errorf("%w: encoded request exceeds the %d-byte limit", ErrRequestTooLarge, maxMarshaledChatRequestBytes)
	}
	return raw, nil
}

func saturatingByteAdd(total, next int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if next > 0 && total > maxInt64-next {
		return maxInt64
	}
	return total + next
}
