package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"e2b/billing-api/internal/usage"
)

const (
	maxBatchBytes      = 1 << 20
	maxBatchEvents     = 1000
	maxIdentifierBytes = 256
)

var eventFields = []string{
	"source", "event_id", "schema_version", "customer_id", "sandbox_id", "metric",
	"period_start", "period_end", "units",
}

// This transport profile prevents permissive parsing of offsets, fractional
// seconds, or calendar representations that would change stored measurements.
var timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

type requestError struct {
	status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type usageBatch struct {
	BatchID string
	Events  []usage.Event
}

type eventInput struct {
	Source        string `json:"source"`
	EventID       string `json:"event_id"`
	SchemaVersion int32  `json:"schema_version"`
	CustomerID    string `json:"customer_id"`
	SandboxID     string `json:"sandbox_id"`
	Metric        string `json:"metric"`
	PeriodStart   string `json:"period_start"`
	PeriodEnd     string `json:"period_end"`
	Units         int64  `json:"units"`
}

func parseUsageBatch(body []byte) (usageBatch, *requestError) {
	if !utf8.Valid(body) || !json.Valid(body) || hasUnpairedSurrogate(body) {
		return usageBatch{}, invalidJSON("The body must contain one valid UTF-8 JSON document.", "")
	}
	fields, err := decodeObject(body, []string{"batch_id", "events"})
	if err != nil {
		return usageBatch{}, invalidJSON(err.Error(), "")
	}
	var batch usageBatch
	if raw, present := fields["batch_id"]; present {
		if isNull(raw) {
			return batch, invalidBatch("batch_id must be a nonblank string when supplied.", "batch_id")
		}
		if err := json.Unmarshal(raw, &batch.BatchID); err != nil {
			return batch, invalidJSON("batch_id must be a JSON string.", "batch_id")
		}
		if strings.TrimSpace(batch.BatchID) == "" || strings.ContainsRune(batch.BatchID, '\x00') || len(batch.BatchID) > maxIdentifierBytes {
			return batch, invalidBatch("batch_id must contain non-whitespace text, no NUL, and at most 256 UTF-8 bytes.", "batch_id")
		}
	}
	rawEvents, present := fields["events"]
	if !present || isNull(rawEvents) {
		return batch, invalidBatch("events is required and must be a nonempty array.", "events")
	}
	var events []json.RawMessage
	if err := json.Unmarshal(rawEvents, &events); err != nil {
		return batch, invalidJSON("events must be a JSON array.", "events")
	}
	if len(events) == 0 || len(events) > maxBatchEvents {
		return batch, invalidBatch("events must contain between 1 and 1000 measurements.", "events")
	}
	batch.Events = make([]usage.Event, 0, len(events))
	for index, raw := range events {
		event, err := parseUsageEvent(raw, fmt.Sprintf("events[%d]", index))
		if err != nil {
			return usageBatch{}, err
		}
		batch.Events = append(batch.Events, event)
	}
	return batch, nil
}

func parseUsageEvent(raw []byte, prefix string) (usage.Event, *requestError) {
	fields, err := decodeObject(raw, eventFields)
	if err != nil {
		return usage.Event{}, invalidJSON(err.Error(), prefix)
	}
	for _, name := range eventFields {
		value, present := fields[name]
		if !present || isNull(value) {
			return usage.Event{}, invalidBatch("The event field is required and must not be null.", prefix+"."+name)
		}
	}
	var input eventInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return usage.Event{}, invalidJSON("Use JSON strings for identifiers and timestamps, and int32/int64 integer tokens for version and units.", prefix)
	}
	event := usage.Event{
		Source: input.Source, EventID: input.EventID, SchemaVersion: input.SchemaVersion,
		CustomerID: input.CustomerID, SandboxID: input.SandboxID, Metric: input.Metric,
		Units: input.Units,
	}
	for _, identifier := range []struct{ name, value string }{
		{"source", input.Source}, {"event_id", input.EventID},
		{"customer_id", input.CustomerID}, {"sandbox_id", input.SandboxID}, {"metric", input.Metric},
	} {
		if len(identifier.value) > maxIdentifierBytes {
			return event, invalidBatch("Identifiers must not exceed 256 UTF-8 bytes.", prefix+"."+identifier.name)
		}
	}
	start, err := parseTimestamp(input.PeriodStart)
	if err != nil {
		return event, invalidBatch(err.Error(), prefix+".period_start")
	}
	end, err := parseTimestamp(input.PeriodEnd)
	if err != nil {
		return event, invalidBatch(err.Error(), prefix+".period_end")
	}
	event.PeriodStart, event.PeriodEnd = start, end
	if err := event.Validate(); err != nil {
		var validationError *usage.ValidationError
		if errors.As(err, &validationError) {
			return event, invalidBatch(validationError.Message, prefix+"."+validationError.Field)
		}
		return event, invalidBatch(err.Error(), prefix)
	}
	return event, nil
}

// decodeObject requires exact member names and rejects duplicate members. The
// standard struct decoder alone would accept case aliases and silently replace
// earlier values, making event identities ambiguous.
func decodeObject(raw []byte, allowed []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("Expected a JSON object.")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("Invalid JSON object.")
		}
		name, ok := token.(string)
		if !ok || !slices.Contains(allowed, name) {
			return nil, errors.New("The object contains an unknown field; names are case-sensitive.")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("The object contains a duplicate field.")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("Invalid JSON field value.")
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("Invalid JSON object.")
	}
	return fields, nil
}

func parseTimestamp(value string) (time.Time, error) {
	if !timestampPattern.MatchString(value) {
		return time.Time{}, errors.New("Use RFC 3339 with an explicit offset and at most six fractional digits.")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("The timestamp must be a valid calendar instant.")
	}
	return parsed.UTC(), nil
}

// hasUnpairedSurrogate checks already-valid JSON, where backslashes only occur
// inside strings. Reject malformed UTF-16 escapes before encoding/json can
// replace them with U+FFFD and collapse distinct producer identities.
func hasUnpairedSurrogate(raw []byte) bool {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			continue
		}
		if raw[index+1] != 'u' {
			index++
			continue
		}
		value, _ := strconv.ParseUint(string(raw[index+2:index+6]), 16, 16)
		if value >= 0xdc00 && value <= 0xdfff {
			return true
		}
		if value >= 0xd800 && value <= 0xdbff {
			if index+12 > len(raw) || raw[index+6] != '\\' || raw[index+7] != 'u' {
				return true
			}
			low, err := strconv.ParseUint(string(raw[index+8:index+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return true
			}
			index += 11
		} else {
			index += 5
		}
	}
	return false
}

func isNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func invalidJSON(message, field string) *requestError {
	return &requestError{400, "invalid_json", message, field}
}

func invalidBatch(message, field string) *requestError {
	return &requestError{422, "invalid_batch", message, field}
}
