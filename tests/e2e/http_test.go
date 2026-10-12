//go:build integration

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type httpStep struct {
	name, method, path, inputBody string
	capture, wantSameAs           string
	wantStatus                    int
	wantBody                      string
	await                         bool
	businessTime                  string
	wantUnchangedState            bool
}
type httpResult struct {
	status int
	body   string
}

// request sends a real HTTP request to the separate API container and checks
// transport errors before the declared status, retaining request/response bytes.
func (e *environment) request(t *testing.T, step httpStep) httpResult {
	t.Helper()
	request, err := http.NewRequest(step.method, "http://api:8080"+step.path, strings.NewReader(step.inputBody))
	if err != nil {
		t.Fatalf("Create %s %s: %v", step.method, step.path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: observationTimeout}).Do(request)
	if err != nil {
		t.Fatalf("%s %s body=%s: %v", step.method, step.path, step.inputBody, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("Read %s %s response: %v", step.method, step.path, err)
	}
	e.save(t, "http.log", fmt.Sprintf("%s: %s %s\ninput=%s\nstatus=%d body=%s\n", step.name, step.method, step.path, step.inputBody, response.StatusCode, body), true)
	if response.StatusCode != step.wantStatus {
		t.Fatalf("%s: %s %s body=%s: status=%d body=%s; want %d", step.name, step.method, step.path, step.inputBody, response.StatusCode, body, step.wantStatus)
	}
	return httpResult{status: response.StatusCode, body: string(body)}
}

// runHTTPSteps executes named explicit HTTP scenarios, including bounded awaits
// and complete rollback assertions. A clock transition always precedes its step.
func (e *environment) runHTTPSteps(t *testing.T, steps []httpStep) bool {
	t.Helper()
	captures := map[string]string{}
	for _, step := range steps {
		passed := t.Run(step.name, func(t *testing.T) {
			if step.businessTime != "" {
				e.advance(t, step.businessTime)
			}
			var before string
			if step.wantUnchangedState {
				before = e.snapshot(t)
			}
			deadline := time.Now().Add(observationTimeout)
			for {
				actual := e.request(t, step)
				err := containsJSON(actual.body, step.wantBody)
				if err == nil {
					if step.wantSameAs != "" {
						previous, found := captures[step.wantSameAs]
						if !found {
							t.Fatalf("Missing captured response %s", step.wantSameAs)
						}
						assertJSON(t, step.name, actual.body, previous)
					}
					if step.capture != "" {
						captures[step.capture] = actual.body
					}
					break
				}
				if !step.await || time.Now().After(deadline) {
					t.Fatalf("%s response=%s; want %s: %v", step.name, actual.body, step.wantBody, err)
				}
				time.Sleep(observationInterval)
			}
			if step.wantUnchangedState && e.snapshot(t) != before {
				t.Fatalf("%s changed complete financial state; expected no changes", step.name)
			}
		})
		if !passed {
			return false
		}
	}
	return true
}

// containsJSON accepts additional generated object fields while comparing all
// declared scalar values and complete ordered arrays with exact JSON numbers.
func containsJSON(actual, want string) error {
	if want == "" {
		return nil
	}
	got, err := decodeJSON(actual)
	if err != nil {
		return err
	}
	expected, err := decodeJSON(want)
	if err != nil {
		return err
	}
	return matchJSON(got, expected, "response")
}

// decodeJSON retains decimal integer lexemes instead of converting API or SQL
// numbers through float64, so independent assertions preserve precision.
func decodeJSON(value string) (any, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	var result any
	err := decoder.Decode(&result)
	return result, err
}

// matchJSON recursively compares declared fields and exact array lengths; it
// performs no financial calculation and explains the first differing location.
func matchJSON(actual, want any, path string) error {
	switch expected := want.(type) {
	case map[string]any:
		object, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("%s=%v; want object", path, actual)
		}
		for key, value := range expected {
			present, found := object[key]
			if !found {
				return fmt.Errorf("%s.%s missing", path, key)
			}
			if err := matchJSON(present, value, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		items, ok := actual.([]any)
		if !ok || len(items) != len(expected) {
			return fmt.Errorf("%s=%v; want %v", path, actual, want)
		}
		for index, value := range expected {
			if err := matchJSON(items[index], value, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	default:
		if !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("%s=%v; want %v", path, actual, want)
		}
	}
	return nil
}
