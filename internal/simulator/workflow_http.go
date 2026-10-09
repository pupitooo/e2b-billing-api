package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type WorkflowRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type WorkflowExpectation struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
	SameAs string          `json:"same_as,omitempty"`
}

type workflowRunner struct {
	options WorkflowOptions
	store   *Store
	state   *workflowState
}
type workflowResponse struct {
	status     int
	body       []byte
	retryDelay time.Duration
}

func (r *workflowRunner) runStep(ctx context.Context, step WorkflowStep) error {
	delay := r.options.RetryMin
	for attempt := 1; r.options.MaxAttempts == 0 || attempt <= r.options.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.state.Attempts++
		response, requestError := r.request(ctx, step)
		retryable := requestError != nil || (response.status != step.Want.Status && (response.status == 408 || response.status == 429 || response.status >= 500))
		err := requestError
		if err == nil {
			err = matchWorkflowResponse(response, step.Want, r.state.Captures)
		}
		if err == nil {
			return r.completeStep(step, response.body)
		}
		if step.Await && response.status == step.Want.Status && !hasUnexpectedProcessingError(response.body, step.Want.Body) {
			retryable = true
		}
		r.state.LastError = fmt.Sprintf("%s: %v", step.Name, err)
		if saveError := r.store.saveJSON(r.state); saveError != nil {
			return saveError
		}
		fmt.Fprintf(r.options.Output, "Step %q attempt %d: %v\n", step.Name, attempt, err)
		if !retryable {
			return fmt.Errorf("step %s: %w", step.Name, err)
		}
		if r.options.MaxAttempts > 0 && attempt == r.options.MaxAttempts {
			break
		}
		wait := delay
		if response.retryDelay > wait {
			wait = response.retryDelay
		}
		if err := (&Sender{}).sleep(ctx, wait); err != nil {
			return err
		}
		if delay >= r.options.RetryMax/2 {
			delay = r.options.RetryMax
		} else {
			delay *= 2
		}
	}
	return fmt.Errorf("step %s exceeded attempt limit: %s", step.Name, r.state.LastError)
}

func (r *workflowRunner) request(ctx context.Context, step WorkflowStep) (workflowResponse, error) {
	escaped, _ := json.Marshal(r.options.Source)
	body := bytes.ReplaceAll(step.Request.Body, []byte("{{source}}"), escaped[1:len(escaped)-1])
	request, err := http.NewRequestWithContext(ctx, step.Request.Method, strings.TrimRight(r.options.BaseURL, "/")+step.Request.Path, bytes.NewReader(body))
	if err != nil {
		return workflowResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.options.Client.Do(request)
	if err != nil {
		return workflowResponse{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return workflowResponse{}, err
	}
	if len(data) > 1<<20 {
		return workflowResponse{}, fmt.Errorf("workflow response exceeds one MiB")
	}
	result := workflowResponse{status: response.StatusCode, body: data, retryDelay: retryAfter(response.Header.Get("Retry-After"))}
	if r.shouldLoseResponse(step, response.StatusCode) {
		r.state.LostSteps[r.state.NextStep] = true
		if r.options.LoseResponse {
			r.state.LostResponseInjected = true
		}
		if err := r.store.saveJSON(r.state); err != nil {
			return result, err
		}
		return result, fmt.Errorf("injected lost committed response; retrying unchanged identity/content")
	}
	return result, nil
}

func (r *workflowRunner) shouldLoseResponse(step WorkflowStep, status int) bool {
	if step.Request.Method != "POST" || (status != 200 && status != 202) || r.state.LostSteps[r.state.NextStep] {
		return false
	}
	return step.Fault.LoseResponse || (r.options.LoseResponse && !r.state.LostResponseInjected)
}

func (r *workflowRunner) completeStep(step WorkflowStep, body []byte) error {
	previous := r.state.NextStep
	if step.Capture != "" {
		r.state.Captures[step.Capture] = append(json.RawMessage(nil), body...)
	}
	r.state.NextStep++
	r.state.LastError = ""
	if err := r.store.saveJSON(r.state); err != nil {
		r.state.NextStep = previous
		return err
	}
	fmt.Fprintf(r.options.Output, "Passed step %q (%s %s, HTTP %d)\n", step.Name, step.Request.Method, step.Request.Path, step.Want.Status)
	return nil
}
