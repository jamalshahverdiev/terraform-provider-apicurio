package provider

import (
	"context"
	"testing"
)

func TestContentEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", `{"a":1}`, `{"a":1}`, true},
		{"json reformatted", `{"type":"record","name":"P","fields":[{"name":"id","type":"string"}]}`,
			"{\n  \"type\": \"record\",\n  \"name\": \"P\",\n  \"fields\": [\n    {\"name\": \"id\", \"type\": \"string\"}\n  ]\n}\n", true},
		{"json key order", `{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{"json different value", `{"a":1}`, `{"a":2}`, false},
		{"json vs text", `{"a":1}`, `syntax = "proto3";`, false},
		{"json number precision kept", `{"a":1.10}`, `{"a":1.1}`, false},
		{"text line endings and trailing space", "syntax = \"proto3\";\r\nmessage P { string id = 1; }  \r\n\r\n", "syntax = \"proto3\";\nmessage P { string id = 1; }\n", true},
		{"text different", "message A {}", "message B {}", false},
		{"yaml reformatted", "openapi: 3.0.3\ninfo:\n  title: T\n  version: '1'\npaths: {}\n", "openapi: \"3.0.3\"\ninfo: {title: T, version: \"1\"}\npaths: {}\n", true},
		{"yaml different", "openapi: 3.0.3\ninfo:\n  title: T\n", "openapi: 3.0.3\ninfo:\n  title: U\n", false},
		{"yaml scalar text is not yaml", "hello world", "hello  world", false},
		{"empty", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := contentEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("contentEqual = %v, want %v", got, tc.want)
			}
			if got := contentEqual(tc.b, tc.a); got != tc.want {
				t.Errorf("contentEqual (swapped) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestContentValueSemanticEquals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := newContent(`{"a": 1}`)
	eq, diags := a.StringSemanticEquals(ctx, newContent("{\"a\":1}\n"))
	if diags.HasError() || !eq {
		t.Errorf("reformatted JSON must be semantically equal: %v %v", eq, diags)
	}
	eq, _ = a.StringSemanticEquals(ctx, contentNull())
	if eq {
		t.Error("a value is not equal to null")
	}
	eq, _ = contentNull().StringSemanticEquals(ctx, contentNull())
	if !eq {
		t.Error("null equals null")
	}
	if !a.Type(ctx).Equal(contentType{}) {
		t.Error("Type() must be contentType")
	}
}

func TestDefaultContentType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, content, artifactType, want string
	}{
		{"avro", `{"type":"record"}`, "AVRO", "application/json"},
		{"json schema", `{"$schema":"x"}`, "JSON", "application/json"},
		{"openapi json", `{"openapi":"3.0.0"}`, "OPENAPI", "application/json"},
		{"openapi yaml", "openapi: 3.0.0\n", "OPENAPI", "application/x-yaml"},
		{"asyncapi yaml", "asyncapi: 2.6.0\n", "ASYNCAPI", "application/x-yaml"},
		{"protobuf", "syntax = \"proto3\";", "PROTOBUF", "application/x-protobuf"},
		{"graphql", "type Query { a: String }", "GRAPHQL", "application/graphql"},
		{"xsd", "<xs:schema/>", "XSD", "application/xml"},
		{"wsdl", "<definitions/>", "WSDL", "application/xml"},
		{"xml", "<a/>", "XML", "application/xml"},
		{"unknown type, json content", `{"a":1}`, "", "application/json"},
		{"unknown type, xml content", "<?xml version=\"1.0\"?><a/>", "", "application/xml"},
		{"unknown type, proto content", "syntax = \"proto3\";\nmessage P {}\n", "", "application/x-protobuf"},
		{"unknown type, proto without syntax line", "package x;\nmessage P {}\n", "", "application/x-protobuf"},
		{"unknown type, graphql content", "type Query {\n  hello: String\n}\n", "", "application/graphql"},
		{"unknown type, yaml content", "openapi: 3.0.3\ninfo:\n  title: T\n", "", "application/x-yaml"},
		{"unknown type, plain text", "hello", "", "text/plain"},
		{"custom type falls back to sniffing", `{"a":1}`, "ODCS_CONTRACT", "application/json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := defaultContentType(tc.content, tc.artifactType); got != tc.want {
				t.Errorf("defaultContentType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMediaType(t *testing.T) {
	t.Parallel()
	if got := mediaType("text/plain;charset=UTF-8"); got != "text/plain" {
		t.Errorf("mediaType = %q", got)
	}
	if got := mediaType(" application/json "); got != "application/json" {
		t.Errorf("mediaType = %q", got)
	}
}

func TestValidateContentForType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, content, artifactType string
		wantErr                     bool
	}{
		{"avro ok", `{"type":"string"}`, "AVRO", false},
		{"avro broken", `{"type":`, "AVRO", true},
		{"kconnect broken", `nope`, "KCONNECT", true},
		{"openapi yaml ok", "openapi: 3.0.0\ninfo: {title: t, version: '1'}\n", "OPENAPI", false},
		{"openapi json ok", `{"openapi":"3.0.0"}`, "OPENAPI", false},
		{"openapi scalar text", "just text", "OPENAPI", true},
		{"xsd ok", `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"/>`, "XSD", false},
		{"xml broken", `<a><b></a>`, "XML", true},
		{"protobuf anything", "whatever", "PROTOBUF", false},
		{"unknown type anything", "whatever", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateContentForType(tc.content, tc.artifactType)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateContentForType = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
