package openapiclient

import (
	"strings"
	"testing"
)

func TestAssembleRequestURLRemovesOnlyTheCompositionSeamSlash(t *testing.T) {
	completed, err := AssembleRequestURL("https://api.example.test/v1/", "/things", "q=a%20b")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := completed.String(), "https://api.example.test/v1/things?q=a%20b"; got != want {
		t.Fatalf("completed URL = %q, want %q", got, want)
	}
}

func TestAssembleRequestURLRejectsInvalidPercentEncoding(t *testing.T) {
	_, err := AssembleRequestURL("https://api.example.test", "/things", "q=%ZZ")
	if err == nil || !strings.Contains(err.Error(), "RFC 3986") {
		t.Fatalf("completed URL error = %v, want RFC 3986 refusal", err)
	}
}
