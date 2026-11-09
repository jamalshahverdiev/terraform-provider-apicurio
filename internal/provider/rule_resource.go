package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// ruleScopeKind selects which of the three rule resources an instance is.
type ruleScopeKind int

const (
	ruleScopeGlobal ruleScopeKind = iota
	ruleScopeGroup
	ruleScopeArtifact
)

var (
	_ resource.Resource                   = &ruleResource{}
	_ resource.ResourceWithConfigure      = &ruleResource{}
	_ resource.ResourceWithImportState    = &ruleResource{}
	_ resource.ResourceWithValidateConfig = &ruleResource{}
)

// NewGlobalRuleResource returns the apicurio_global_rule resource.
func NewGlobalRuleResource() resource.Resource { return &ruleResource{kind: ruleScopeGlobal} }

// NewGroupRuleResource returns the apicurio_group_rule resource.
func NewGroupRuleResource() resource.Resource { return &ruleResource{kind: ruleScopeGroup} }

// NewArtifactRuleResource returns the apicurio_artifact_rule resource.
func NewArtifactRuleResource() resource.Resource { return &ruleResource{kind: ruleScopeArtifact} }

// ruleTypes lists the rule types in the order the documentation uses.
var ruleTypes = []string{client.RuleValidity, client.RuleCompatibility, client.RuleIntegrity}

// ruleConfigs lists the config values of each rule type. INTEGRITY takes a
// comma-separated set of its values; the registry stores any string, so the
// provider validates.
var ruleConfigs = map[string][]string{
	client.RuleValidity:      {"NONE", "SYNTAX_ONLY", "FULL"},
	client.RuleCompatibility: {"NONE", "BACKWARD", "BACKWARD_TRANSITIVE", "FORWARD", "FORWARD_TRANSITIVE", "FULL", "FULL_TRANSITIVE"},
	client.RuleIntegrity:     {"NONE", "REFS_EXIST", "NO_DUPLICATES", "NO_CIRCULAR_REFERENCES", "ALL_REFS_MAPPED", "FULL"},
}

// ruleConfigProblem explains why config is not valid for ruleType ("" when it is).
func ruleConfigProblem(ruleType, config string) string {
	allowed, ok := ruleConfigs[ruleType]
	if !ok {
		return "" // the rule_type validator reports it
	}
	if ruleType != client.RuleIntegrity {
		if slices.Contains(allowed, config) {
			return ""
		}
		return fmt.Sprintf("%s rules take one of %s; got %q.", ruleType, backquoted(allowed), config)
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(config, ",") {
		level := strings.TrimSpace(part)
		if level == "" {
			return "INTEGRITY rules take a comma-separated set of levels without empty entries."
		}
		if !slices.Contains(allowed, level) {
			return fmt.Sprintf("INTEGRITY rules take a comma-separated set of %s; got %q.", backquoted(allowed), level)
		}
		if seen[level] {
			return fmt.Sprintf("INTEGRITY level %s is listed more than once.", level)
		}
		seen[level] = true
	}
	if len(seen) > 1 && (seen["NONE"] || seen["FULL"]) {
		return "INTEGRITY levels NONE and FULL stand alone and cannot be combined with other levels."
	}
	return ""
}

func backquoted(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = "`" + v + "`"
	}
	return strings.Join(q, ", ")
}

type ruleResource struct {
	kind   ruleScopeKind
	client *client.Client
}

// ruleModel holds the attributes of a rule resource; group_id and artifact_id
// exist only in the schemas of the group and artifact rules.
type ruleModel struct {
	ID, GroupID, ArtifactID, RuleType, Config types.String
}

// attributeGetter is what tfsdk.Config, tfsdk.Plan and tfsdk.State offer.
type attributeGetter interface {
	GetAttribute(ctx context.Context, p path.Path, target any) diag.Diagnostics
}

func (r *ruleResource) typeName() string {
	switch r.kind {
	case ruleScopeGroup:
		return "group_rule"
	case ruleScopeArtifact:
		return "artifact_rule"
	}
	return "global_rule"
}

// noun is the resource's name in diagnostics.
func (r *ruleResource) noun() string {
	switch r.kind {
	case ruleScopeGroup:
		return "Apicurio Group Rule"
	case ruleScopeArtifact:
		return "Apicurio Artifact Rule"
	}
	return "Apicurio Global Rule"
}

func (r *ruleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.typeName()
}

func (r *ruleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	var description, idForm string
	switch r.kind {
	case ruleScopeGlobal:
		description = "A registry-wide rule, applied to every artifact that has no rule of the same type on its group or on itself. " +
			"One resource per rule type; a rule configured outside Terraform can be imported. On a secured registry global rules " +
			"need the admin role."
		idForm = "`rule_type`"
	case ruleScopeGroup:
		description = "A rule on a group, applied to every artifact of the group that has no rule of the same type itself; it takes " +
			"precedence over the global rule of that type. The group must exist (an `apicurio_group`). On a secured registry " +
			"group rules need the developer role."
		idForm = "`group_id/rule_type`"
	case ruleScopeArtifact:
		description = "A rule on one artifact; it takes precedence over the group and global rules of that type, and `config = \"NONE\"` " +
			"switches the type off for the artifact even when a global or group rule is set. The artifact must exist " +
			"(an `apicurio_artifact`). On a secured registry artifact rules need the developer role."
		idForm = "`group_id/artifact_id/rule_type`"
	}
	description += " Rules are applied when a version is created and when a draft leaves `DRAFT`, never to existing versions. " +
		"See the provider documentation for the precedence of rules and their maturity per artifact type."

	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed:            true,
			MarkdownDescription: idForm + ".",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"rule_type": schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "`VALIDITY`, `COMPATIBILITY` or `INTEGRITY`. Changing it replaces the rule.",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators:          []validator.String{stringvalidator.OneOf(ruleTypes...)},
		},
		"config": schema.StringAttribute{
			Required: true,
			MarkdownDescription: "Configuration of the rule, validated against `rule_type`: `VALIDITY` takes `NONE`, `SYNTAX_ONLY` or `FULL`; " +
				"`COMPATIBILITY` takes `NONE`, `BACKWARD`, `BACKWARD_TRANSITIVE`, `FORWARD`, `FORWARD_TRANSITIVE`, `FULL` or " +
				"`FULL_TRANSITIVE`; `INTEGRITY` takes a comma-separated set of `REFS_EXIST`, `NO_DUPLICATES`, " +
				"`NO_CIRCULAR_REFERENCES` and `ALL_REFS_MAPPED`, or `FULL` or `NONE` alone. Changed in place.",
		},
	}
	if r.kind != ruleScopeGlobal {
		attrs["group_id"] = schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Group the rule belongs to. Changing it replaces the rule.",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators:          []validator.String{stringvalidator.LengthBetween(1, 512), stringvalidator.RegexMatches(noSlashRe, "must not contain a slash")},
		}
	}
	if r.kind == ruleScopeArtifact {
		attrs["artifact_id"] = schema.StringAttribute{
			Required:            true,
			MarkdownDescription: "Artifact the rule belongs to. Changing it replaces the rule.",
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Validators:          []validator.String{stringvalidator.LengthBetween(1, 512)},
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: description, Attributes: attrs}
}

func (r *ruleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *ruleResource) getModel(ctx context.Context, src attributeGetter) (ruleModel, diag.Diagnostics) {
	var m ruleModel
	var diags diag.Diagnostics
	diags.Append(src.GetAttribute(ctx, path.Root("id"), &m.ID)...)
	if r.kind != ruleScopeGlobal {
		diags.Append(src.GetAttribute(ctx, path.Root("group_id"), &m.GroupID)...)
	}
	if r.kind == ruleScopeArtifact {
		diags.Append(src.GetAttribute(ctx, path.Root("artifact_id"), &m.ArtifactID)...)
	}
	diags.Append(src.GetAttribute(ctx, path.Root("rule_type"), &m.RuleType)...)
	diags.Append(src.GetAttribute(ctx, path.Root("config"), &m.Config)...)
	return m, diags
}

func (r *ruleResource) setState(ctx context.Context, st *tfsdk.State, m ruleModel) diag.Diagnostics {
	var diags diag.Diagnostics
	diags.Append(st.SetAttribute(ctx, path.Root("id"), types.StringValue(r.id(m)))...)
	if r.kind != ruleScopeGlobal {
		diags.Append(st.SetAttribute(ctx, path.Root("group_id"), m.GroupID)...)
	}
	if r.kind == ruleScopeArtifact {
		diags.Append(st.SetAttribute(ctx, path.Root("artifact_id"), m.ArtifactID)...)
	}
	diags.Append(st.SetAttribute(ctx, path.Root("rule_type"), m.RuleType)...)
	diags.Append(st.SetAttribute(ctx, path.Root("config"), m.Config)...)
	return diags
}

func (r *ruleResource) scope(m ruleModel) client.RuleScope {
	return client.RuleScope{GroupID: m.GroupID.ValueString(), ArtifactID: m.ArtifactID.ValueString()}
}

func (r *ruleResource) id(m ruleModel) string {
	switch r.kind {
	case ruleScopeGroup:
		return m.GroupID.ValueString() + "/" + m.RuleType.ValueString()
	case ruleScopeArtifact:
		return m.GroupID.ValueString() + "/" + m.ArtifactID.ValueString() + "/" + m.RuleType.ValueString()
	}
	return m.RuleType.ValueString()
}

func (r *ruleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	m, diags := r.getModel(ctx, req.Config)
	resp.Diagnostics.Append(diags...)
	// An empty config is validated too (the registry refuses it at apply time).
	if resp.Diagnostics.HasError() || !isSet(m.RuleType) || m.Config.IsNull() || m.Config.IsUnknown() {
		return
	}
	if problem := ruleConfigProblem(m.RuleType.ValueString(), m.Config.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(path.Root("config"), "Invalid Rule Configuration", problem)
	}
}

func (r *ruleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	m, diags := r.getModel(ctx, req.Plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	scope := r.scope(m)
	err := r.client.CreateRule(ctx, scope, client.Rule{RuleType: m.RuleType.ValueString(), Config: m.Config.ValueString()})
	if err != nil {
		switch {
		case errors.Is(err, client.ErrAlreadyExists):
			resp.Diagnostics.AddError(r.noun()+" Already Exists",
				fmt.Sprintf("A %s rule is already configured for %s. Import it instead:\n\n  terraform import apicurio_%s.<name> %s",
					m.RuleType.ValueString(), scope, r.typeName(), r.id(m)))
		case client.IsNotFound(err) && r.kind == ruleScopeArtifact:
			resp.Diagnostics.AddError("Apicurio Artifact Does Not Exist",
				fmt.Sprintf("Artifact %q does not exist. Create it first (apicurio_artifact) and reference it from the rule.\n\n%s", scope.GroupID+"/"+scope.ArtifactID, err))
		case client.IsNotFound(err) && r.kind == ruleScopeGroup:
			resp.Diagnostics.AddError("Apicurio Group Does Not Exist",
				fmt.Sprintf("Group %q does not exist. Create it first (apicurio_group) and reference it from the rule.\n\n%s", scope.GroupID, err))
		default:
			resp.Diagnostics.AddError("Unable to Create "+r.noun(), err.Error())
		}
		return
	}
	rule, err := r.client.GetRule(ctx, scope, m.RuleType.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read "+r.noun()+" After Creation", err.Error())
		return
	}
	m.Config = types.StringValue(rule.Config)
	resp.Diagnostics.Append(r.setState(ctx, &resp.State, m)...)
}

func (r *ruleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	m, diags := r.getModel(ctx, req.State)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.client.GetRule(ctx, r.scope(m), m.RuleType.ValueString())
	if err != nil {
		if client.IsNotFound(err) { // the rule, the group or the artifact is gone
			tflog.Warn(ctx, "rule no longer exists, removing it from state", map[string]any{"id": r.id(m)})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read "+r.noun(), err.Error())
		return
	}
	m.Config = types.StringValue(rule.Config)
	resp.Diagnostics.Append(r.setState(ctx, &resp.State, m)...)
}

func (r *ruleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	m, diags := r.getModel(ctx, req.Plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.client.UpdateRule(ctx, r.scope(m), client.Rule{RuleType: m.RuleType.ValueString(), Config: m.Config.ValueString()})
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError(r.noun()+" Disappeared",
				fmt.Sprintf("Rule %q no longer exists (the rule, the group or the artifact was deleted); run terraform apply again to recreate it.", r.id(m)))
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Update "+r.noun(), err.Error())
		return
	}
	m.Config = types.StringValue(rule.Config)
	resp.Diagnostics.Append(r.setState(ctx, &resp.State, m)...)
}

func (r *ruleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	m, diags := r.getModel(ctx, req.State)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteRule(ctx, r.scope(m), m.RuleType.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete "+r.noun(), err.Error())
	}
}

func (r *ruleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	var m ruleModel
	var ok bool
	var groupID, artifactID, ruleType string
	switch r.kind {
	case ruleScopeGlobal:
		ruleType, ok = req.ID, !strings.Contains(req.ID, "/")
	case ruleScopeGroup:
		groupID, ruleType, ok = splitGroupArtifact(req.ID)
		ok = ok && !strings.Contains(ruleType, "/")
	case ruleScopeArtifact:
		groupID, artifactID, ruleType, ok = splitGroupArtifactVersion(req.ID)
	}
	if !ok || !slices.Contains(ruleTypes, ruleType) {
		var form string
		switch r.kind {
		case ruleScopeGlobal:
			form = "`RULE_TYPE`"
		case ruleScopeGroup:
			form = "`group_id/RULE_TYPE`"
		case ruleScopeArtifact:
			form = "`group_id/artifact_id/RULE_TYPE`"
		}
		resp.Diagnostics.AddError("Invalid Import ID",
			fmt.Sprintf("Expected %s where RULE_TYPE is one of %s, got %q.", form, backquoted(ruleTypes), req.ID))
		return
	}
	m.GroupID, m.ArtifactID, m.RuleType = types.StringValue(groupID), types.StringValue(artifactID), types.StringValue(ruleType)
	m.Config = types.StringNull()
	resp.Diagnostics.Append(r.setState(ctx, &resp.State, m)...)
}
