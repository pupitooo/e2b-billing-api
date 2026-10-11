package simulator

import (
	"e2b/billing-api/internal/usage"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	minHTTPStatus = http.StatusContinue
	maxHTTPStatus = 599
)

func validateWorkflowStep(step WorkflowStep) error {
	if step.BusinessTime != "" {
		instant, err := time.Parse(time.RFC3339, step.BusinessTime)
		if err != nil || instant.Location() != time.UTC || instant.Year() < usage.MinUTCYear || instant.Year() > usage.MaxUTCYear {
			return fmt.Errorf("step %s: business_time must be a supported UTC RFC3339 instant", step.Name)
		}
	}

	if step.Request.Method != "GET" && step.Request.Method != "POST" {
		return fmt.Errorf("step %s: method must be GET or POST", step.Name)
	}

	if !validWorkflowPath(step.Request.Path) || step.Want.Status < minHTTPStatus || step.Want.Status > maxHTTPStatus {
		return fmt.Errorf("step %s: invalid relative path or expected status", step.Name)
	}

	if step.Await && step.Request.Method != "GET" {
		return fmt.Errorf("step %s: only reads can await accounting", step.Name)
	}

	return nil
}

func validWorkflowPath(value string) bool {
	path, err := url.ParseRequestURI(value)
	if err != nil || path.IsAbs() || path.Host != "" {
		return false
	}

	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.Contains(value, "#")
}

func registerWorkflowCapture(step WorkflowStep, captures map[string]bool) error {
	if step.Want.SameAs != "" && !captures[step.Want.SameAs] {
		return fmt.Errorf("step %s: response capture must precede its comparison", step.Name)
	}

	if step.Capture == "" {
		return nil
	}

	if captures[step.Capture] {
		return fmt.Errorf("step %s: capture names must be unique", step.Name)
	}

	captures[step.Capture] = true

	return nil
}
