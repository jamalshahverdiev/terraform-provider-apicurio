package provider

import (
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// Short aliases used by the resources and data sources.
type (
	diagsT    = diag.Diagnostics
	attrValue = attr.Value
)

var noSlashRe = regexp.MustCompile(`^[^/]+$`)

func joinMax(parts []string) string { return strings.Join(parts, ", ") }
