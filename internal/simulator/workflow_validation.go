package simulator

import (
	"fmt"
	"net/url"
	"strings"
)

func validateWorkflowStep(step WorkflowStep) error {
	if step.Request.Method != "GET" && step.Request.Method != "POST" {
		return fmt.Errorf("step %s: method must be GET or POST", step.Name)
	}

	if !validWorkflowPath(step.Request.Path) || step.Want.Status < 100 || step.Want.Status > 599 {
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
