package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// stringFromServer maps a server string to state: an empty server value
// stays null when the configuration never set the attribute (prior null or
// unknown), and becomes "" when the configuration set it to "".
func stringFromServer(prior types.String, s string) types.String {
	if s == "" && (prior.IsNull() || prior.IsUnknown()) {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// labelsFromServer maps server labels to state with the same null/empty
// rule as stringFromServer.
func labelsFromServer(ctx context.Context, prior types.Map, labels map[string]string) (types.Map, diag.Diagnostics) {
	if len(labels) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.MapNull(types.StringType), nil
		}
		return types.MapValueMust(types.StringType, map[string]attr.Value{}), nil
	}
	return types.MapValueFrom(ctx, types.StringType, labels)
}

// labelsToServer converts the configured labels; null and unknown become an
// empty map, which the registry stores as "no labels".
func labelsToServer(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	out := map[string]string{}
	if m.IsNull() || m.IsUnknown() {
		return out, nil
	}
	diags := m.ElementsAs(ctx, &out, false)
	return out, diags
}

// referenceModel is one element of the `references` attribute.
type referenceModel struct {
	GroupID    types.String `tfsdk:"group_id"`
	ArtifactID types.String `tfsdk:"artifact_id"`
	Version    types.String `tfsdk:"version"`
	Name       types.String `tfsdk:"name"`
}

var referenceAttrTypes = map[string]attr.Type{
	"group_id":    types.StringType,
	"artifact_id": types.StringType,
	"version":     types.StringType,
	"name":        types.StringType,
}

var referenceObjectType = types.ObjectType{AttrTypes: referenceAttrTypes}

func referencesFromServer(ctx context.Context, prior types.List, refs []client.Reference) (types.List, diag.Diagnostics) {
	if len(refs) == 0 {
		if prior.IsNull() || prior.IsUnknown() {
			return types.ListNull(referenceObjectType), nil
		}
		return types.ListValueMust(referenceObjectType, []attr.Value{}), nil
	}
	models := make([]referenceModel, 0, len(refs))
	for _, r := range refs {
		models = append(models, referenceModel{
			GroupID:    types.StringValue(r.GroupID),
			ArtifactID: types.StringValue(r.ArtifactID),
			Version:    types.StringValue(r.Version),
			Name:       types.StringValue(r.Name),
		})
	}
	return types.ListValueFrom(ctx, referenceObjectType, models)
}

func referencesToServer(ctx context.Context, l types.List) ([]client.Reference, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var models []referenceModel
	diags := l.ElementsAs(ctx, &models, false)
	if diags.HasError() {
		return nil, diags
	}
	refs := make([]client.Reference, 0, len(models))
	for _, m := range models {
		refs = append(refs, client.Reference{
			GroupID:    m.GroupID.ValueString(),
			ArtifactID: m.ArtifactID.ValueString(),
			Version:    m.Version.ValueString(),
			Name:       m.Name.ValueString(),
		})
	}
	return refs, diags
}

// referencesEqual compares two reference sets regardless of order.
func referencesEqual(a, b []client.Reference) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(r client.Reference) string {
		return strings.Join([]string{r.GroupID, r.ArtifactID, r.Version, r.Name}, "\x00")
	}
	ka := make([]string, 0, len(a))
	kb := make([]string, 0, len(b))
	for _, r := range a {
		ka = append(ka, key(r))
	}
	for _, r := range b {
		kb = append(kb, key(r))
	}
	sort.Strings(ka)
	sort.Strings(kb)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// splitGroupArtifact parses an import id of the form `group_id/artifact_id`.
// The group id cannot contain a slash; the artifact id may.
func splitGroupArtifact(id string) (groupID, artifactID string, ok bool) {
	i := strings.Index(id, "/")
	if i <= 0 || i == len(id)-1 {
		return "", "", false
	}
	return id[:i], id[i+1:], true
}

// splitGroupArtifactVersion parses `group_id/artifact_id/<last>` where the
// last segment is a version or branch id (which cannot contain a slash) and
// the artifact id in the middle may.
func splitGroupArtifactVersion(id string) (groupID, artifactID, last string, ok bool) {
	i := strings.Index(id, "/")
	j := strings.LastIndex(id, "/")
	if i <= 0 || j <= i+1 || j == len(id)-1 {
		return "", "", "", false
	}
	return id[:i], id[i+1 : j], id[j+1:], true
}

// listToStrings converts a list of strings; unknown elements become "".
func listToStrings(l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(l.Elements()))
	for _, e := range l.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() {
			out = append(out, "")
			continue
		}
		out = append(out, s.ValueString())
	}
	return out
}

// stringLike is satisfied by types.String and by the custom content value.
type stringLike interface {
	IsNull() bool
	IsUnknown() bool
	ValueString() string
}

// isSet reports whether a string attribute carries a non-empty known value.
func isSet(v stringLike) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueString() != ""
}

// operationDisabledDetail renders a 405 "operation disabled" error with the
// resource that can switch the property on.
func operationDisabledDetail(err error) (string, bool) {
	var de *client.OperationDisabledError
	if !errors.As(err, &de) {
		return "", false
	}
	return fmt.Sprintf("%s\n\nTerraform can switch it on with:\n\n  resource \"apicurio_config_property\" \"%s\" {\n    name  = %q\n    value = \"true\"\n  }\n\nand a depends_on from the resources that need it.",
		de.Error(), strings.ReplaceAll(de.Operation, " ", "_"), de.Property), true
}

// isUnknownTypeError recognises the registry's answer to content whose
// artifact type it cannot discover.
func isUnknownTypeError(err error) bool {
	var ae *client.APIError
	return errors.As(err, &ae) && ae.Name == "InvalidArtifactTypeException"
}
