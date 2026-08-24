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

// The strings below are verbatim from the raw archive (2026-08-24), not
// invented: the gate exists because these exact phrases were being counted as
// technologies, and a synthetic phrasing would not have caught them.
func TestExtractGatesAmbiguousAliases(t *testing.T) {
	tax := LoadTaxonomy([][3]string{
		{"go", "go", "language"}, {"golang", "go", "language"},
		{"express", "expressjs", "framework"}, {"expressjs", "expressjs", "framework"},
		{"python", "python", "language"},
	})
	tests := []struct {
		name string
		text string
		slug string
		want bool
	}{
		// real mentions — a separator on one side or the other
		{"go slash list", "at least one programming language (Go/Python preferred) in Linux", "go", true},
		{"go path list", "1 or more programming languages such as C/C++/Go/Python/Java", "go", true},
		{"go comma list", "Proficiency in Python, Go, or Shell scripting", "go", true},
		{"go spaced slashes", "one systems-programming language among Java / Go / Rust / C++", "go", true},
		{"go end of sentence", "scripting skills in Python, Bash, Shell, or Go. Hands-on experience", "go", true},
		{"go before paren", "e.g., Python, PowerShell, Bash, Java, C#, or Go). Solid understanding", "go", true},
		{"express dot js", "Strong proficiency in Node.js, TypeScript, and Express.js or NestJS", "expressjs", true},
		{"express in slash list", "expose them via REST/gRPC APIs (FastAPI/Express/Java/Go)", "expressjs", true},
		{"express in comma list", "Spring Boot, Angular, Vue, Next.js, Express, or FastAPI", "expressjs", true},

		// English words that are not technologies
		{"go to market", "Drive product launches and go-to-market strategies", "go", false},
		{"go live", "configuration, testing, deployment, and go-live", "go", false},
		{"go getter", "a winning attitude Self-motivated go-getters", "go", false},
		{"go beyond", "build practical skills that go beyond the classroom", "go", false},
		{"go somewhere", "Start somewhere. Go somewhere. Grow into bigger responsibilities", "go", false},
		{"go on trip", "Able to go on business trip within short notice", "go", false},
		{"go the extra mile", "A willingness to learn and go the extra mile", "go", false},
		{"go to reviewer", "act as the go-to reviewer for a defined group", "go", false},
		{"express themselves", "built to help people authentically express themselves, discover and connect", "expressjs", false},
		{"agency name", "EA Licence No: 99C4599 Recruit Express Pte Ltd Network Engineer", "expressjs", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasSlug(tax.Extract(tt.text), tt.slug); got != tt.want {
				t.Errorf("Extract(%q) has %q = %v, want %v", tt.text, tt.slug, got, tt.want)
			}
		})
	}
}

// golang/expressjs must stay reachable without a separator: the gate applies to
// the ambiguous spelling only, so the unambiguous ones still match in prose.
func TestExtractUnambiguousSpellingsNotGated(t *testing.T) {
	tax := LoadTaxonomy([][3]string{
		{"go", "go", "language"}, {"golang", "go", "language"},
		{"express", "expressjs", "framework"}, {"expressjs", "expressjs", "framework"},
	})
	if !hasSlug(tax.Extract("strong Golang experience required"), "go") {
		t.Error("golang in prose should match")
	}
	if !hasSlug(tax.Extract("we run ExpressJS in production"), "expressjs") {
		t.Error("expressjs in prose should match")
	}
}

// NormalizeTerm receives one already-isolated term from the LLM layer, where
// the word-sense ambiguity cannot arise — gating must not break it.
func TestNormalizeTermAcceptsBareAmbiguousAlias(t *testing.T) {
	tax := LoadTaxonomy([][3]string{
		{"go", "go", "language"}, {"express", "expressjs", "framework"},
	})
	for term, want := range map[string]string{"Go": "go", "go": "go", "Express": "expressjs"} {
		slug, _, ok := tax.NormalizeTerm(term)
		if !ok || slug != want {
			t.Errorf("NormalizeTerm(%q) = %q, %v; want %q, true", term, slug, ok, want)
		}
	}
}
