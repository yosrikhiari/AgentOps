package router

import (
	"strings"
	"testing"
)

func TestPIIDetector_IBAN(t *testing.T) {
	d := NewPIIDetector()
	text := "My IBAN is FR7630006000011234567890189"
	found := d.Detect(text)
	if len(found) == 0 {
		t.Fatal("expected IBAN detection")
	}
}

func TestPIIDetector_Email(t *testing.T) {
	d := NewPIIDetector()
	text := "Contact me at user@example.com"
	found := d.Detect(text)
	hasEmail := false
	for _, f := range found {
		if f == "email" {
			hasEmail = true
		}
	}
	if !hasEmail {
		t.Fatalf("expected email detection, got %v", found)
	}
}

func TestPIIDetector_Phone(t *testing.T) {
	d := NewPIIDetector()
	text := "Call me at 555-123-4567"
	found := d.Detect(text)
	hasPhone := false
	for _, f := range found {
		if f == "phone" {
			hasPhone = true
		}
	}
	if !hasPhone {
		t.Fatalf("expected phone detection, got %v", found)
	}
}

func TestPIIDetector_Clean(t *testing.T) {
	d := NewPIIDetector()
	text := "What is the weather today?"
	found := d.Detect(text)
	if len(found) > 0 {
		t.Fatalf("expected no PII, got %v", found)
	}
}

func TestSovereigntyPolicy_LocalOnly(t *testing.T) {
	p := NewSovereigntyPolicy()
	err := p.Check("hello", PolicyLocalOnly)
	if err == nil {
		t.Fatal("expected error for local_only")
	}
}

func TestSovereigntyPolicy_CloudOKClean(t *testing.T) {
	p := NewSovereigntyPolicy()
	err := p.Check("What is the weather?", PolicyCloudOK)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestSovereigntyPolicy_CloudOKPII(t *testing.T) {
	p := NewSovereigntyPolicy()
	err := p.Check("My email is user@example.com", PolicyCloudOK)
	if err == nil {
		t.Fatal("expected error for PII")
	}
	if !strings.Contains(err.Error(), "PII") {
		t.Fatalf("expected PII in error, got %v", err)
	}
}
