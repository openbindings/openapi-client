package openapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openbindings/openapi-client/go/openapi"
)

// Swagger 2.0's Swagger Object requires a host without a scheme or path,
// and a nonempty basePath beginning with '/'. Invalid fields must not alter
// the authority or become server variables.
func TestReviewRepairSwaggerAuthority(t *testing.T) {
	for _, extra := range []string{
		`"host":"api.example.test","basePath":"v1"`,
		`"host":"api.example.test","basePath":".elsewhere.test/v1"`,
		`"host":"https://api.example.test"`,
		`"host":"api.example.test/v2"`,
		`"host":"api.example.test","basePath":"/{tenant}"`,
	} {
		doc := editionDoc("2.0", `"/x":{"get":{"responses":{"200":{"description":"ok"}}}}`, extra, `"schemes":["https"]`)
		c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
		if err != nil {
			continue
		}
		if _, err := c.Prepare("GET /x", nil); err == nil {
			t.Errorf("accepted invalid Swagger server: %s", extra)
		}
	}
	for _, host := range []string{"api.example.test", "api.example.test:443", "[::1]:8080"} {
		doc := editionDoc("2.0", `"/x":{"get":{"responses":{"200":{"description":"ok"}}}}`, fmt.Sprintf(`"host":%q,"basePath":"/v1"`, host), `"schemes":["https"]`)
		c, err := openapi.Parse(t.Context(), []byte(doc), testDocURI, nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.Prepare("GET /x", nil)
		if err != nil || r.HTTP.URL.Host != host || r.HTTP.URL.Path != "/v1/x" {
			t.Fatalf("host %q: request=%v err=%v", host, r, err)
		}
	}
}

// Reproduce the numeric-conversion amplification without a multi-gigabyte
// allocation. Four times the input may not cost quadratic allocation.
func TestReviewRepairOctalAllocation(t *testing.T) {
	measure := func(n int) uint64 {
		doc := []byte("openapi: 3.1.0\npaths: {}\nx-number: 0o" + strings.Repeat("7", n) + "\n")
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		runtime.KeepAlive(c)
		return after.TotalAlloc - before.TotalAlloc
	}
	small, large := measure(64<<10), measure(256<<10)
	if large > small*8 {
		t.Fatalf("4x octal input allocated %d vs %d bytes (%.2fx)", large, small, float64(large)/float64(small))
	}
}

// Exercise actual Windows path handling, not just a URL constructed on Unix.
func TestReviewRepairWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filesystem paths")
	}
	dir := t.TempDir()
	entry := filepath.Join(dir, "api.json")
	doc := `{"openapi":"3.1.0","paths":{},"components":{"schemas":{"A":{"$ref":"other.json"}}}}`
	if err := os.WriteFile(entry, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{"type":"string"}`), 0600); err != nil {
		t.Fatal(err)
	}
	u := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(entry)}).String()
	for _, name := range []string{entry, filepath.ToSlash(entry), u} {
		c, err := openapi.Load(t.Context(), name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.DocumentURIs()) != 2 {
			t.Fatalf("%s: loaded %v", name, c.DocumentURIs())
		}
		for _, uri := range c.DocumentURIs() {
			if !strings.HasPrefix(uri, "file:///") {
				t.Errorf("invalid drive URL %q", uri)
			}
		}
	}
}

// Compare the packed conversion against math/big on bounded numbers,
// covering both 32- and 64-bit word boundaries and leading zeros.
func TestReviewRepairOctalValues(t *testing.T) {
	for n := 1; n <= 257; n++ {
		digits := strings.Repeat("01234567", (n+7)/8)[:n]
		want, ok := new(big.Int).SetString(digits, 8)
		if !ok {
			t.Fatal("invalid test fixture")
		}
		c := parsed(t, []byte(yamlHead+"x-number: 0o"+digits+"\n"))
		var got struct {
			Number json.Number `json:"x-number"`
		}
		if err := json.Unmarshal(c.Document(""), &got); err != nil {
			t.Fatal(err)
		}
		if got.Number.String() != want.String() {
			t.Fatalf("octal %s: got %s want %s", digits, got.Number, want)
		}
	}
}

func TestReviewRepairYAMLErrorLocation(t *testing.T) {
	for _, src := range []string{yamlHead + "x-m: [one, *unknown]\n", yamlHead + "x-m: [one, {\n", yamlHead + "x-m: a: b\n"} {
		for _, data := range [][]byte{[]byte(src), utf16Text(src, false, true), utf32Text(src, true, true)} {
			_, err := openapi.Parse(t.Context(), data, testDocURI, nil)
			if err == nil {
				t.Fatal("accepted malformed YAML")
			}
			if strings.Contains(err.Error(), "yaml: line ") {
				t.Fatalf("contradictory parser location: %v", err)
			}
		}
	}
}

func BenchmarkReviewOctal(b *testing.B) {
	for _, n := range []int{64 << 10, 256 << 10, 4 << 20} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			doc := []byte(yamlHead + "x-number: 0o" + strings.Repeat("7", n) + "\n")
			b.ReportAllocs()
			b.SetBytes(int64(len(doc)))
			b.ResetTimer()
			for b.Loop() {
				c, err := openapi.Parse(context.Background(), doc, testDocURI, nil)
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(c)
			}
		})
	}
}

func TestReviewRepairVirtualFilesExplicitAdmission(t *testing.T) {
	f := &zzFS{docs: map[string]string{
		"file:///virtual-review/api.json":   `{"openapi":"3.1.0","paths":{},"components":{"schemas":{"A":{"$ref":"model.json"}}}}`,
		"file:///virtual-review/model.json": `{"type":"string"}`,
	}}
	l := openapi.Loader{Fetch: f.fetch, AllowReference: func(from, to string) bool { return strings.HasPrefix(to, "file:///virtual-review/") }}
	c, err := l.Load(t.Context(), "file:///virtual-review/api.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.DocumentURIs()) != 2 {
		t.Fatalf("custom admission did not load in-memory files: %v", c.DocumentURIs())
	}
}
