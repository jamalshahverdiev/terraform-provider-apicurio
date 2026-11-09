package provider

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"gopkg.in/yaml.v3"
)

// contentType is the attribute type of artifact content. Two values are
// equal when they are the same document: parsed JSON for JSON content,
// parsed YAML for YAML content, otherwise the text with line endings and
// trailing whitespace normalised. Reformatting a schema file is therefore
// not a change, while the stored bytes stay exactly what was sent.
type contentType struct {
	basetypes.StringType
}

var (
	_ basetypes.StringTypable                    = contentType{}
	_ basetypes.StringValuableWithSemanticEquals = contentValue{}
)

func (t contentType) Equal(o attr.Type) bool {
	_, ok := o.(contentType)
	return ok
}

func (t contentType) String() string { return "contentType" }

func (t contentType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return contentValue{StringValue: in}, nil
}

func (t contentType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	v, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	sv, ok := v.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T for content", v)
	}
	return contentValue{StringValue: sv}, nil
}

func (t contentType) ValueType(context.Context) attr.Value { return contentValue{} }

// contentValue is a string value compared with contentEqual.
type contentValue struct {
	basetypes.StringValue
}

func newContent(s string) contentValue { return contentValue{StringValue: basetypes.NewStringValue(s)} }
func contentNull() contentValue        { return contentValue{StringValue: basetypes.NewStringNull()} }

func (v contentValue) Type(context.Context) attr.Type { return contentType{} }

func (v contentValue) Equal(o attr.Value) bool {
	other, ok := o.(contentValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals reports whether the two contents are the same document.
func (v contentValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	other, ok := newValuable.(contentValue)
	if !ok {
		diags.AddError("Semantic Equality Check Error",
			fmt.Sprintf("Expected a content value, got %T. This is a bug in the provider.", newValuable))
		return false, diags
	}
	if v.IsNull() || v.IsUnknown() || other.IsNull() || other.IsUnknown() {
		return v.StringValue.Equal(other.StringValue), diags
	}
	return contentEqual(v.ValueString(), other.ValueString()), diags
}

// contentEqual compares two documents: JSON as parsed JSON, otherwise the
// normalised text, otherwise parsed YAML.
func contentEqual(a, b string) bool {
	if a == b {
		return true
	}
	if ja, ok := parseJSON(a); ok {
		jb, ok := parseJSON(b)
		return ok && reflect.DeepEqual(ja, jb)
	}
	if _, ok := parseJSON(b); ok {
		return false
	}
	if normalizeText(a) == normalizeText(b) {
		return true
	}
	ya, ok := parseYAML(a)
	if !ok {
		return false
	}
	yb, ok := parseYAML(b)
	return ok && reflect.DeepEqual(ya, yb)
}

func parseJSON(s string) (any, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[' && trimmed[0] != '"') {
		return nil, false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	if dec.More() {
		return nil, false
	}
	return v, true
}

// parseYAML accepts only documents whose root is a mapping or a sequence, so
// that arbitrary text (a .proto file is a valid YAML scalar) is not treated
// as YAML.
func parseYAML(s string) (any, bool) {
	var v any
	if err := yaml.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	switch v.(type) {
	case map[string]any, []any:
		return v, true
	}
	return nil, false
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// Artifact type families.
var (
	jsonTypes = map[string]bool{"AVRO": true, "JSON": true, "KCONNECT": true}
	yamlTypes = map[string]bool{"OPENAPI": true, "ASYNCAPI": true}
	xmlTypes  = map[string]bool{"XSD": true, "WSDL": true, "XML": true}
)

var (
	protobufRe = regexp.MustCompile(`(?m)^\s*(syntax\s*=|package\s+[\w.]+\s*;|message\s+\w+|import\s+"|service\s+\w+)`)
	graphqlRe  = regexp.MustCompile(`(?m)^\s*(type|schema|input|enum|interface|scalar|union|directive|extend|query|mutation|subscription|fragment)\b`)
)

// defaultContentType returns the content type to send when the
// configuration does not set one. The registry detects the artifact type
// from the content only with a fitting hint (application/x-protobuf for
// Protobuf, application/x-yaml for YAML, application/graphql for GraphQL,
// application/xml for XML), so the sniffing matters when artifact_type is
// unset.
func defaultContentType(content, artifactType string) string {
	switch {
	case jsonTypes[artifactType]:
		return "application/json"
	case yamlTypes[artifactType]:
		if _, ok := parseJSON(content); ok {
			return "application/json"
		}
		return "application/x-yaml"
	case artifactType == "PROTOBUF":
		return "application/x-protobuf"
	case artifactType == "GRAPHQL":
		return "application/graphql"
	case xmlTypes[artifactType]:
		return "application/xml"
	}
	trimmed := strings.TrimSpace(content)
	switch {
	case trimmed == "":
		return "text/plain"
	case parseable(parseJSON, content):
		return "application/json"
	case strings.HasPrefix(trimmed, "<"):
		return "application/xml"
	case protobufRe.MatchString(content):
		return "application/x-protobuf"
	case graphqlRe.MatchString(content):
		return "application/graphql"
	case parseable(parseYAML, content):
		return "application/x-yaml"
	}
	return "text/plain"
}

func parseable(f func(string) (any, bool), s string) bool {
	_, ok := f(s)
	return ok
}

// mediaType strips parameters from a Content-Type header value
// ("text/plain;charset=UTF-8" -> "text/plain").
func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// validateContentForType checks that content is parseable in the family of
// a known artifact type. The registry itself stores anything when no
// VALIDITY rule is configured.
func validateContentForType(content, artifactType string) error {
	switch {
	case jsonTypes[artifactType]:
		if !json.Valid([]byte(content)) {
			return fmt.Errorf("content of an %s artifact must be valid JSON", artifactType)
		}
	case yamlTypes[artifactType]:
		if _, ok := parseJSON(content); ok {
			return nil
		}
		if _, ok := parseYAML(content); !ok {
			return fmt.Errorf("content of an %s artifact must be a JSON or YAML document", artifactType)
		}
	case xmlTypes[artifactType]:
		dec := xml.NewDecoder(strings.NewReader(content))
		for {
			_, err := dec.Token()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("content of an %s artifact must be well-formed XML: %w", artifactType, err)
			}
		}
	}
	return nil
}
