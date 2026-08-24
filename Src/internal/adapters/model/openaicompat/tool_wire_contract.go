package openaicompat

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const maxProviderToolNameBytes = 64

// providerToolNameCodec keeps durable/runtime tool identities unchanged while
// adapting them to the OpenAI-compatible wire contract. Some providers reject
// function names longer than 64 bytes; MCP canonical names intentionally carry
// a capability UUID and can exceed that limit.
type providerToolNameCodec struct {
	canonicalToWire map[string]string
	wireToCanonical map[string]string
}

func newProviderToolNameCodec() *providerToolNameCodec {
	return &providerToolNameCodec{
		canonicalToWire: make(map[string]string),
		wireToCanonical: make(map[string]string),
	}
}

func (codec *providerToolNameCodec) encode(canonical string) string {
	canonical = strings.TrimSpace(canonical)
	if canonical == "" {
		return ""
	}
	if wire, exists := codec.canonicalToWire[canonical]; exists {
		return wire
	}
	wire := canonical
	if len(wire) > maxProviderToolNameBytes {
		wire = compactProviderToolName(canonical)
	}
	if existing, collision := codec.wireToCanonical[wire]; collision && existing != canonical {
		wire = compactProviderToolNameWithHashBytes(canonical, 24)
	}
	codec.canonicalToWire[canonical] = wire
	codec.wireToCanonical[wire] = canonical
	return wire
}

func (codec *providerToolNameCodec) decode(wire string) string {
	wire = strings.TrimSpace(wire)
	if canonical, exists := codec.wireToCanonical[wire]; exists {
		return canonical
	}
	return wire
}

func compactProviderToolName(canonical string) string {
	return compactProviderToolNameWithHashBytes(canonical, 16)
}

func compactProviderToolNameWithHashBytes(canonical string, hashBytes int) string {
	digest := sha256.Sum256([]byte(canonical))
	hash := hex.EncodeToString(digest[:])[:hashBytes]
	if strings.HasPrefix(canonical, "mcp__") {
		parts := strings.SplitN(strings.TrimPrefix(canonical, "mcp__"), "__", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			prefix := "mcp__" + hash + "__"
			return prefix + boundedToolNameSuffix(parts[1], maxProviderToolNameBytes-len(prefix), hash)
		}
	}
	prefix := "tool_" + hash + "_"
	return prefix + boundedToolNameSuffix(canonical, maxProviderToolNameBytes-len(prefix), hash)
}

func boundedToolNameSuffix(value string, limit int, hash string) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	const separator = "_"
	tail := hash
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	prefixLimit := limit - len(separator) - len(tail)
	if prefixLimit <= 0 {
		return tail[:limit]
	}
	return value[:prefixLimit] + separator + tail
}

// $schema selects a JSON Schema dialect but does not change validation
// semantics. OpenAI-compatible gateways vary on whether that metadata keyword
// is accepted inside function parameters, so remove it only on the wire copy.
func providerToolParameters(parameters map[string]any) map[string]any {
	if len(parameters) == 0 {
		return parameters
	}
	return providerSchemaValue(parameters).(map[string]any)
}

func providerSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copyValue := make(map[string]any, len(typed))
		for key, child := range typed {
			if key == "$schema" {
				continue
			}
			copyValue[key] = providerSchemaValue(child)
		}
		return copyValue
	case []any:
		copyValue := make([]any, len(typed))
		for index, child := range typed {
			copyValue[index] = providerSchemaValue(child)
		}
		return copyValue
	default:
		return value
	}
}
