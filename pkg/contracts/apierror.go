package contracts

import (
	"fmt"
	"strings"
)

// FieldError is one validation failure, tied to the field that caused it.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError is the API error shape every mads service returns for
// invalid input (spec §10.3): validate everything, report every error
// together — never fail on the first problem. Serialized as
// {"errors":[{"field":..,"message":..},..]} with HTTP 422.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

// Error implements error.
func (v *ValidationError) Error() string {
	parts := make([]string, len(v.Errors))
	for i, e := range v.Errors {
		parts[i] = fmt.Sprintf("%s: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("validation failed: %s", strings.Join(parts, "; "))
}

// Add appends a field error. Use one ValidationError per request, Add every
// failure found, then check Empty at the end of validation.
func (v *ValidationError) Add(field, message string) {
	v.Errors = append(v.Errors, FieldError{Field: field, Message: message})
}

// Empty reports whether validation found no errors.
func (v *ValidationError) Empty() bool {
	return len(v.Errors) == 0
}
