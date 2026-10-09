package simulator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

func matchWorkflowResponse(response workflowResponse, want WorkflowExpectation, captures map[string]json.RawMessage) error {
	if response.status != want.Status {
		return fmt.Errorf("HTTP status=%d want %d; response=%s", response.status, want.Status, response.body)
	}
	actual, err := workflowJSON(response.body)
	if err != nil {
		return err
	}
	if want.SameAs != "" {
		captured, present := captures[want.SameAs]
		if !present {
			return fmt.Errorf("missing captured response %s", want.SameAs)
		}
		previous, err := workflowJSON(captured)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, previous) {
			return fmt.Errorf("immutable response differs from capture %s", want.SameAs)
		}
	}
	if len(want.Body) == 0 {
		return nil
	}
	expected, err := workflowJSON(want.Body)
	if err != nil {
		return err
	}
	return containsJSON(actual, expected, "response")
}

func workflowJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("response must contain exactly one JSON value")
	}
	return value, nil
}

// containsJSON permits extra generated fields in objects but requires literal
// scalar values, array length, and array order. It never computes financial results.
func containsJSON(actual, expected any, path string) error {
	switch wanted := expected.(type) {
	case map[string]any:
		object, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not an object", path)
		}
		for name, value := range wanted {
			present, ok := object[name]
			if !ok {
				return fmt.Errorf("%s.%s is missing", path, name)
			}
			if err := containsJSON(present, value, path+"."+name); err != nil {
				return err
			}
		}
	case []any:
		items, ok := actual.([]any)
		if !ok || len(items) != len(wanted) {
			return fmt.Errorf("%s array=%v want %v", path, actual, expected)
		}
		for i, value := range wanted {
			if err := containsJSON(items[i], value, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("%s=%v want %v", path, actual, expected)
		}
	}
	return nil
}

func hasUnexpectedProcessingError(actual, wanted []byte) bool {
	var value, expected struct {
		ProcessingErrors int64 `json:"processing_errors"`
	}
	if json.Unmarshal(actual, &value) != nil || json.Unmarshal(wanted, &expected) != nil {
		return false
	}
	return value.ProcessingErrors > expected.ProcessingErrors
}
