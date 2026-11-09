package provider

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

var (
	_ resource.Resource                = &configPropertyResource{}
	_ resource.ResourceWithConfigure   = &configPropertyResource{}
	_ resource.ResourceWithImportState = &configPropertyResource{}
	_ resource.ResourceWithModifyPlan  = &configPropertyResource{}
)

// NewConfigPropertyResource returns the apicurio_config_property resource.
func NewConfigPropertyResource() resource.Resource { return &configPropertyResource{} }

type configPropertyResource struct {
	client *client.Client
}

type configPropertyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Value       types.String `tfsdk:"value"`
	Type        types.String `tfsdk:"type"`
	Label       types.String `tfsdk:"label"`
	Description types.String `tfsdk:"description"`
}

// Property types the registry reports.
const (
	propertyTypeBoolean = "java.lang.Boolean"
	propertyTypeInteger = "java.lang.Integer"
	propertyTypeLong    = "java.lang.Long"
)

// propertyValueProblem explains why value does not fit a property type ("" when it does).
func propertyValueProblem(typ, value string) string {
	switch typ {
	case propertyTypeBoolean:
		if v := strings.ToLower(value); v != "true" && v != "false" {
			return fmt.Sprintf("a boolean property takes \"true\" or \"false\", got %q.", value)
		}
	case propertyTypeInteger:
		if _, err := strconv.ParseInt(value, 10, 32); err != nil {
			return fmt.Sprintf("an integer property takes a whole number, got %q.", value)
		}
	case propertyTypeLong:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Sprintf("a long property takes a whole number, got %q.", value)
		}
	}
	return ""
}

func (r *configPropertyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config_property"
}

func (r *configPropertyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A dynamic configuration property of the registry, one of those `GET /admin/config/properties` lists " +
			"(for example the deletion switches `apicurio.rest.deletion.group.enabled`, `apicurio.rest.deletion.artifact.enabled` " +
			"and `apicurio.rest.deletion.artifact-version.enabled`). The change applies at once. Destroying the resource resets the " +
			"property to the server's default (its environment variable or shipped default); it does not restore the value " +
			"found before Terraform managed it. Properties that are not dynamic, such as " +
			"`apicurio.storage.enable-automatic-group-creation`, can only be set through the server's environment. " +
			"Needs the admin role on a secured registry. Make the resources that rely on a switch `depends_on` this resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The property name.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Name of the dynamic property. Changing it replaces the resource (the old property is reset).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"value": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Value as a string, checked against the property's `type` at plan time: `true`/`false` for a boolean, " +
					"a whole number for an integer or long. Stored and compared as written.",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Java type of the property: `java.lang.Boolean`, `java.lang.Integer`, `java.lang.Long` or `java.lang.String`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"label": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Label the registry shows for the property.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Description the registry gives for the property.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *configPropertyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

// ModifyPlan checks the name and the value against the registry at plan
// time, so that an unknown property or a value of the wrong type is reported
// before anything is applied. A registry that cannot be reached is left to
// the apply.
func (r *configPropertyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan configPropertyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !isSet(plan.Name) {
		return
	}
	prop, err := r.client.GetConfigProperty(ctx, plan.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.Append(r.notDynamicError(ctx, plan.Name.ValueString())...)
			return
		}
		tflog.Debug(ctx, "property lookup at plan time failed, deferring to apply", map[string]any{"error": err.Error()})
		return
	}
	set := func(p string, v string) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(p), types.StringValue(v))...)
	}
	set("type", prop.Type)
	set("label", prop.Label)
	set("description", prop.Description)
	if isSet(plan.Value) {
		if problem := propertyValueProblem(prop.Type, plan.Value.ValueString()); problem != "" {
			resp.Diagnostics.AddAttributeError(path.Root("value"), "Invalid Property Value",
				fmt.Sprintf("Property %q is a %s: %s", prop.Name, prop.Type, problem))
		}
	}
}

func (r *configPropertyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configPropertyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.write(ctx, plan.Name.ValueString(), plan.Value.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, found, diags := r.read(ctx, plan.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Configuration Property After Creation", fmt.Sprintf("Property %q was set but could not be read back.", plan.Name.ValueString()))
		return
	}
	// The registry stores the value verbatim; keeping the written value makes a
	// concurrent writer (another resource for the same name) a drift on the
	// next plan instead of an "inconsistent result" error.
	state.Value = plan.Value
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *configPropertyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior configPropertyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, found, diags := r.read(ctx, prior.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		tflog.Warn(ctx, "configuration property is no longer dynamic, removing it from state", map[string]any{"name": prior.Name.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *configPropertyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configPropertyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.write(ctx, plan.Name.ValueString(), plan.Value.ValueString())...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, found, diags := r.read(ctx, plan.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Configuration Property After Update", fmt.Sprintf("Property %q was set but could not be read back.", plan.Name.ValueString()))
		return
	}
	state.Value = plan.Value
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *configPropertyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configPropertyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.ResetConfigProperty(ctx, state.Name.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to Reset Apicurio Configuration Property", err.Error())
	}
}

func (r *configPropertyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" || strings.Contains(req.ID, "/") {
		resp.Diagnostics.AddError("Invalid Import ID", fmt.Sprintf("Expected the name of a dynamic property, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}

// write checks the property and its value against the registry, then sets it.
func (r *configPropertyResource) write(ctx context.Context, name, value string) diagsT {
	var diags diagsT
	prop, err := r.client.GetConfigProperty(ctx, name)
	if err != nil {
		if client.IsNotFound(err) {
			return r.notDynamicError(ctx, name)
		}
		diags.AddError("Unable to Read Apicurio Configuration Property", err.Error())
		return diags
	}
	if problem := propertyValueProblem(prop.Type, value); problem != "" {
		diags.AddAttributeError(path.Root("value"), "Invalid Property Value", fmt.Sprintf("Property %q is a %s: %s", name, prop.Type, problem))
		return diags
	}
	if err := r.client.SetConfigProperty(ctx, name, value); err != nil {
		var ae *client.APIError
		if errorsAs(err, &ae) && ae.Name == "InvalidPropertyValueException" {
			diags.AddAttributeError(path.Root("value"), "Apicurio Registry Refused the Property Value", err.Error())
			return diags
		}
		diags.AddError("Unable to Set Apicurio Configuration Property", err.Error())
	}
	return diags
}

// notDynamicError names the dynamic properties the registry does have.
func (r *configPropertyResource) notDynamicError(ctx context.Context, name string) diagsT {
	var diags diagsT
	known := "could not be listed"
	if props, err := r.client.ListConfigProperties(ctx); err == nil {
		names := make([]string, 0, len(props))
		for _, p := range props {
			names = append(names, p.Name)
		}
		sort.Strings(names)
		known = strings.Join(names, ", ")
	}
	diags.AddAttributeError(path.Root("name"), "Not a Dynamic Property of the Apicurio Registry",
		fmt.Sprintf("The registry at %s has no dynamic configuration property %q. Dynamic properties of this registry: %s. "+
			"Other settings (for example apicurio.storage.enable-automatic-group-creation) can only be set through the server's environment.",
			r.client.ServerURL(), name, known))
	return diags
}

func (r *configPropertyResource) read(ctx context.Context, name string) (configPropertyModel, bool, diagsT) {
	var diags diagsT
	prop, err := r.client.GetConfigProperty(ctx, name)
	if err != nil {
		if client.IsNotFound(err) {
			return configPropertyModel{}, false, diags
		}
		diags.AddError("Unable to Read Apicurio Configuration Property", err.Error())
		return configPropertyModel{}, false, diags
	}
	return configPropertyModel{
		ID:          types.StringValue(prop.Name),
		Name:        types.StringValue(prop.Name),
		Value:       types.StringValue(prop.Value),
		Type:        types.StringValue(prop.Type),
		Label:       types.StringValue(prop.Label),
		Description: types.StringValue(prop.Description),
	}, true, diags
}
