package provider

import (
	"context"
	"errors"
	"fmt"
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
	_ resource.Resource                = &roleMappingResource{}
	_ resource.ResourceWithConfigure   = &roleMappingResource{}
	_ resource.ResourceWithImportState = &roleMappingResource{}
)

// NewRoleMappingResource returns the apicurio_role_mapping resource.
func NewRoleMappingResource() resource.Resource { return &roleMappingResource{} }

type roleMappingResource struct {
	client *client.Client
}

type roleMappingModel struct {
	ID            types.String `tfsdk:"id"`
	PrincipalID   types.String `tfsdk:"principal_id"`
	Role          types.String `tfsdk:"role"`
	PrincipalName types.String `tfsdk:"principal_name"`
}

func (r *roleMappingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role_mapping"
}

func (r *roleMappingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The registry role of a principal, on a registry that manages roles itself: " +
			"`apicurio.auth.role-based-authorization=true` and `apicurio.auth.role-source=application`. Without that, every " +
			"role mapping request is refused (HTTP 403) and the provider reports it. The principal is the `preferred_username` " +
			"claim of a user or the name of a service account (`service-account-<client id>`), as the registry sees it in " +
			"`apicurio_current_user`. A principal without a mapping can read nothing and write nothing; the server's admin override " +
			"(`apicurio.auth.admin-override.enabled`) is how the first admin, usually the provider's own credentials, gets in. " +
			"Needs the admin role.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The principal id.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"principal_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "User or service account id as the token names it. Changing it replaces the mapping.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"role": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`READ_ONLY`, `DEVELOPER` or `ADMIN`. Changed in place.",
				Validators:          []validator.String{stringvalidator.OneOf(client.RoleReadOnly, client.RoleDeveloper, client.RoleAdmin)},
			},
			"principal_name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Display name stored with the mapping. The registry cannot change it, so a change replaces the mapping.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *roleMappingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFromProviderData(req.ProviderData, &resp.Diagnostics)
}

func (r *roleMappingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan roleMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	principal := plan.PrincipalID.ValueString()
	// The registry answers 409 to a duplicate, but a GET first gives the
	// import hint without relying on the message.
	_, err := r.client.GetRoleMapping(ctx, principal)
	switch {
	case err == nil:
		resp.Diagnostics.AddError("Apicurio Role Mapping Already Exists",
			fmt.Sprintf("Principal %q is already mapped. Import it instead:\n\n  terraform import apicurio_role_mapping.<name> %s", principal, principal))
		return
	case client.IsNotFound(err):
	case client.IsForbidden(err):
		resp.Diagnostics.Append(r.forbiddenError(err)...)
		return
	default:
		resp.Diagnostics.AddError("Unable to Read Apicurio Role Mapping", err.Error())
		return
	}
	in := client.RoleMapping{PrincipalID: principal, Role: plan.Role.ValueString()}
	if isSet(plan.PrincipalName) {
		in.PrincipalName = plan.PrincipalName.ValueString()
	}
	if err := r.client.CreateRoleMapping(ctx, in); err != nil {
		switch {
		case errors.Is(err, client.ErrAlreadyExists):
			resp.Diagnostics.AddError("Apicurio Role Mapping Already Exists",
				fmt.Sprintf("Principal %q is already mapped. Import it instead:\n\n  terraform import apicurio_role_mapping.<name> %s", principal, principal))
		case client.IsForbidden(err):
			resp.Diagnostics.Append(r.forbiddenError(err)...)
		default:
			resp.Diagnostics.AddError("Unable to Create Apicurio Role Mapping", err.Error())
		}
		return
	}
	state, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Role Mapping After Creation", fmt.Sprintf("The mapping of %q was created but could not be read back.", principal))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *roleMappingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var prior roleMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, found, diags := r.read(ctx, prior)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		tflog.Warn(ctx, "role mapping no longer exists, removing it from state", map[string]any{"principal_id": prior.PrincipalID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *roleMappingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan roleMappingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	principal := plan.PrincipalID.ValueString()
	if err := r.client.UpdateRoleMapping(ctx, principal, plan.Role.ValueString()); err != nil {
		switch {
		case client.IsNotFound(err):
			resp.Diagnostics.AddError("Apicurio Role Mapping Disappeared", fmt.Sprintf("Principal %q is no longer mapped; run terraform apply again to recreate the mapping.", principal))
			resp.State.RemoveResource(ctx)
		case client.IsForbidden(err):
			resp.Diagnostics.Append(r.forbiddenError(err)...)
		default:
			resp.Diagnostics.AddError("Unable to Update Apicurio Role Mapping", err.Error())
		}
		return
	}
	state, found, diags := r.read(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError("Unable to Read Apicurio Role Mapping After Update", fmt.Sprintf("The mapping of %q could not be read back.", principal))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *roleMappingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state roleMappingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.client.DeleteRoleMapping(ctx, state.PrincipalID.ValueString())
	switch {
	case err == nil, client.IsNotFound(err):
	case client.IsForbidden(err):
		resp.Diagnostics.Append(r.forbiddenError(err)...)
	default:
		resp.Diagnostics.AddError("Unable to Delete Apicurio Role Mapping", err.Error())
	}
}

func (r *roleMappingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) == "" {
		resp.Diagnostics.AddError("Invalid Import ID", "Expected the principal id of a role mapping.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("principal_id"), req.ID)...)
}

// forbiddenError explains a 403 on the role mapping endpoints. The server
// says "Application RBAC not enabled." when it does not manage roles itself
// (3.0.x: an empty body) and "User is not authorized ..." when the caller
// merely lacks the admin role.
func (r *roleMappingResource) forbiddenError(err error) diagsT {
	var diags diagsT
	var ae *client.APIError
	if errors.As(err, &ae) && ae.Title != "" && !strings.Contains(ae.Title, "RBAC not enabled") {
		diags.AddError("Apicurio Registry Refused the Role Mapping Request",
			fmt.Sprintf("The registry at %s answered HTTP 403: the provider's credentials lack the admin role that role mappings "+
				"need (check them with the apicurio_current_user data source).\n\n%s", r.client.ServerURL(), err))
		return diags
	}
	diags.AddError("Role Mappings Are Not Available on the Apicurio Registry",
		fmt.Sprintf("The registry at %s refused the role mapping request (HTTP 403). Role mappings exist only when the server "+
			"runs with apicurio.auth.role-based-authorization=true and apicurio.auth.role-source=application, and the provider's "+
			"credentials must have the admin role (check with the apicurio_current_user data source).\n\n%s", r.client.ServerURL(), err))
	return diags
}

func (r *roleMappingResource) read(ctx context.Context, prior roleMappingModel) (roleMappingModel, bool, diagsT) {
	var diags diagsT
	principal := prior.PrincipalID.ValueString()
	m, err := r.client.GetRoleMapping(ctx, principal)
	if err != nil {
		switch {
		case client.IsNotFound(err):
			return roleMappingModel{}, false, diags
		case client.IsForbidden(err):
			diags.Append(r.forbiddenError(err)...)
		default:
			diags.AddError("Unable to Read Apicurio Role Mapping", err.Error())
		}
		return roleMappingModel{}, false, diags
	}
	return roleMappingModel{
		ID:            types.StringValue(principal),
		PrincipalID:   types.StringValue(principal),
		Role:          types.StringValue(m.Role),
		PrincipalName: stringFromServer(prior.PrincipalName, m.PrincipalName),
	}, true, diags
}
