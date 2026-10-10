package simulator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
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

	expected, err = resolveCapturedFields(expected, captures)
	if err != nil {
		return err
	}

	if err := orderExpectedUsageLines(expected, want.UsageLineOrder); err != nil {
		return err
	}

	return containsJSON(actual, expected, "response")
}

// resolveCapturedFields substitutes only complete references to previously
// returned fields. Financial expectations remain literal scenario values.
func resolveCapturedFields(value any, captures map[string]json.RawMessage) (any, error) {
	switch node := value.(type) {
	case map[string]any:
		for field, child := range node {
			resolved, err := resolveCapturedFields(child, captures)
			if err != nil {
				return nil, err
			}

			node[field] = resolved
		}
	case []any:
		for index, child := range node {
			resolved, err := resolveCapturedFields(child, captures)
			if err != nil {
				return nil, err
			}

			node[index] = resolved
		}
	case string:
		if !strings.HasPrefix(node, "{{capture.") || !strings.HasSuffix(node, "}}") {
			return value, nil
		}

		name, field, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(node, "{{capture."), "}}"), ".")
		data, present := captures[name]
		if !ok || !present {
			return nil, fmt.Errorf("missing captured field reference %s", node)
		}

		captured, err := workflowJSON(data)
		if err != nil {
			return nil, err
		}

		object, ok := captured.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("capture %s is not an object", name)
		}

		result, present := object[field]
		if !present {
			return nil, fmt.Errorf("capture %s has no field %s", name, field)
		}

		return result, nil
	}

	return value, nil
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

// orderExpectedUsageLines retains exact array comparison when a scenario's
// invoice order depends on generated IDs. Only the leading usage lines are
// ordered; all financial values and non-usage lines remain literal expectations.
func orderExpectedUsageLines(expected any, order string) error {
	if order == "" {
		return nil
	}

	if order != "price_version_id" {
		return fmt.Errorf("unsupported expected usage line order %s", order)
	}

	object, ok := expected.(map[string]any)
	if !ok {
		return fmt.Errorf("ordered invoice expectation must be an object")
	}

	lines, ok := object["lines"].([]any)
	if !ok {
		return fmt.Errorf("ordered invoice expectation requires lines")
	}

	usageCount := 0
	for _, line := range lines {
		fields, ok := line.(map[string]any)
		if !ok {
			return fmt.Errorf("expected invoice line must be an object")
		}

		if fields["kind"] != "usage" {
			break
		}

		if _, ok := fields["price_version_id"].(string); !ok {
			return fmt.Errorf("ordered usage line requires a captured price ID")
		}

		usageCount++
	}

	sort.SliceStable(lines[:usageCount], func(i, j int) bool {
		return lines[i].(map[string]any)["price_version_id"].(string) < lines[j].(map[string]any)["price_version_id"].(string)
	})

	return nil
}
