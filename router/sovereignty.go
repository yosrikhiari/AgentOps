package router

import (
	"fmt"
	"regexp"
	"strings"
)

// DataPolicy controls where a key's data may go.
type DataPolicy string

const (
	PolicyLocalOnly DataPolicy = "local_only"
	PolicyCloudOK   DataPolicy = "cloud_ok"
)

// PIIDetector scans text for PII patterns.
type PIIDetector struct {
	patterns map[string]*regexp.Regexp
}

// NewPIIDetector creates a detector with built-in patterns.
func NewPIIDetector() *PIIDetector {
	return &PIIDetector{
		patterns: map[string]*regexp.Regexp{
			"iban":   regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{4}\d{7}([A-Z0-9]?){0,16}\b`),
			"email":  regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`),
			"phone":  regexp.MustCompile(`\b(\+?\d{1,3}[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`),
			"ssn":    regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			"credit": regexp.MustCompile(`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`),
		},
	}
}

// Detect returns the list of PII types found in text.
func (d *PIIDetector) Detect(text string) []string {
	var found []string
	for name, re := range d.patterns {
		if re.MatchString(text) {
			found = append(found, name)
		}
	}
	// Keyword sets (French + English)
	lower := strings.ToLower(text)
	keywords := []string{"iban", "carte bancaire", "numéro de sécurité sociale", "social security",
		"credit card", "password", "mot de passe", "confidential", "secret"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			found = append(found, "keyword:"+kw)
		}
	}
	return found
}

// HasPII reports whether text contains any PII.
func (d *PIIDetector) HasPII(text string) bool {
	return len(d.Detect(text)) > 0
}

// SovereigntyPolicy checks if a request may use a cloud backend.
type SovereigntyPolicy struct {
	detector *PIIDetector
}

// NewSovereigntyPolicy creates a policy with the given detector.
func NewSovereigntyPolicy() *SovereigntyPolicy {
	return &SovereigntyPolicy{detector: NewPIIDetector()}
}

// Check returns nil if the request may proceed to cloud, or an error if blocked.
func (p *SovereigntyPolicy) Check(text string, policy DataPolicy) error {
	if policy == PolicyLocalOnly {
		return fmt.Errorf("data_policy=local_only: cloud backend not allowed")
	}
	if p.detector.HasPII(text) {
		return fmt.Errorf("PII detected: %v", p.detector.Detect(text))
	}
	return nil
}

// AuditEntry records a blocked request.
type AuditEntry struct {
	KeyID     int
	Reason    string
	PIITypes  []string
	Timestamp string
}
