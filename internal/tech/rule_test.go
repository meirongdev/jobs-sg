package tech

import (
	"reflect"
	"testing"
)

func sampleRows() [][3]string {
	return [][3]string{
		{"go", "go", "language"}, {"golang", "go", "language"},
		{"kubernetes", "kubernetes", "tool"}, {"k8s", "kubernetes", "tool"},
		{"gcp", "google-cloud", "cloud"}, {"google cloud", "google-cloud", "cloud"},
		{"c++", "cpp", "language"}, {"c#", "csharp", "language"},
		{"python", "python", "language"}, {"py", "python", "language"},
		{"react", "react", "framework"},
		{"node.js", "nodejs", "language"},
	}
}

func hasSlug(ts []Tech, slug string) bool {
	for _, t := range ts {
		if t.Slug == slug {
			return true
		}
	}
	return false
}

func TestExtractNormalizesAliases(t *testing.T) {
	tax := LoadTaxonomy(sampleRows())
	got := tax.Extract("We use golang, k8s and gcp in our stack.")
	want := []string{"go", "google-cloud", "kubernetes"}
	if !reflect.DeepEqual(slugsOf(got), want) {
		t.Errorf("got %v, want %v", slugsOf(got), want)
	}
}

func TestExtractNoSubstringFalsePositives(t *testing.T) {
	tax := LoadTaxonomy(sampleRows())
	got := tax.Extract("python developer writing golang")
	// "golang" normalises to slug go; "python" must NOT produce slug "py"
	if hasSlug(got, "py") {
		t.Errorf("py matched inside python: %v", slugsOf(got))
	}
	if !hasSlug(got, "go") || !hasSlug(got, "python") {
		t.Errorf("expected go(python's golang) and python, got %v", slugsOf(got))
	}
}

func TestExtractHandlesNonWordBoundaries(t *testing.T) {
	tax := LoadTaxonomy(sampleRows())
	got := tax.Extract("C++ and C# programmer, loves Node.js")
	want := []string{"cpp", "csharp", "nodejs"}
	if !reflect.DeepEqual(slugsOf(got), want) {
		t.Errorf("got %v, want %v", slugsOf(got), want)
	}
}

func TestExtractDeduplicatesBySlug(t *testing.T) {
	tax := LoadTaxonomy(sampleRows())
	got := tax.Extract("Go and golang, k8s and kubernetes")
	want := []string{"go", "kubernetes"}
	if !reflect.DeepEqual(slugsOf(got), want) {
		t.Errorf("got %v, want %v", slugsOf(got), want)
	}
}

func TestExtractNilTaxonomy(t *testing.T) {
	var tax *Taxonomy
	if got := tax.Extract("anything"); got != nil {
		t.Errorf("nil taxonomy should return nil, got %v", got)
	}
}

func TestStripHTML(t *testing.T) {
	in := "<p>Hello&nbsp;<b>Go</b> developer\n with  spaces</p> &amp; more"
	got := StripHTML(in)
	if got != "Hello Go developer with spaces & more" {
		t.Errorf("StripHTML = %q", got)
	}
}

func slugsOf(ts []Tech) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Slug)
	}
	return out
}
