package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// fakeRoleSourceProperty is a property only the fake registry has: it decides
// whether the fake serves role mappings (application RBAC) or answers 403.
const fakeRoleSourceProperty = "apicurio.auth.role-source"

func propertyConfig(name, value string) string {
	return fmt.Sprintf(`
resource "apicurio_config_property" "test" {
  name  = %q
  value = %q
}
`, name, value)
}

// rbacAvailable reports whether the registry serves role mappings.
func rbacAvailable(t *testing.T, c *client.Client) bool {
	t.Helper()
	_, err := c.GetRoleMapping(context.Background(), randName("tfacc-probe"))
	switch {
	case client.IsNotFound(err):
		return true
	case client.IsForbidden(err):
		return false
	case err != nil:
		t.Fatalf("probe role mappings: %v", err)
	}
	return true
}

func TestAccConfigPropertyResource_basic(t *testing.T) {
	c := setupRegistry(t)
	const name = "apicurio.ccompat.max-subjects"
	res := "apicurio_config_property.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			// destroying resets the property to the server default
			p, err := c.GetConfigProperty(context.Background(), name)
			if err != nil {
				return err
			}
			if p.Value != "1000" {
				return fmt.Errorf("%s = %q after destroy, want the default 1000", name, p.Value)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: propertyConfig(name, "42"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", name),
					resource.TestCheckResourceAttr(res, "value", "42"),
					resource.TestCheckResourceAttr(res, "type", "java.lang.Integer"),
					resource.TestCheckResourceAttrSet(res, "label"),
					func(_ *terraform.State) error {
						p, err := c.GetConfigProperty(context.Background(), name)
						if err != nil {
							return err
						}
						if p.Value != "42" {
							return fmt.Errorf("registry has %s = %q, want 42", name, p.Value)
						}
						return nil
					},
				),
			},
			{
				Config: propertyConfig(name, "7"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(res, "value", "7"),
			},
			{Config: propertyConfig(name, "7"), PlanOnly: true},
			{
				// changed outside Terraform: drift is detected
				PreConfig: func() {
					if err := c.SetConfigProperty(context.Background(), name, "500"); err != nil {
						t.Fatalf("set outside terraform: %v", err)
					}
				},
				Config: propertyConfig(name, "7"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(res, "value", "7"),
			},
			{
				ResourceName:      res,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccConfigPropertyResource_validation(t *testing.T) {
	setupRegistry(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// a boolean property takes true/false, checked at plan time
				Config:      propertyConfig("apicurio.rest.deletion.group.enabled", "maybe"),
				ExpectError: regexp.MustCompile(`Invalid Property Value[\s\S]*java\.lang\.Boolean[\s\S]*"maybe"`),
			},
			{
				Config:      propertyConfig("apicurio.ccompat.max-subjects", "many"),
				ExpectError: regexp.MustCompile(`Invalid Property Value[\s\S]*whole number`),
			},
			{
				// known to the server but not dynamic
				Config:      propertyConfig("apicurio.storage.enable-automatic-group-creation", "false"),
				ExpectError: regexp.MustCompile(`Not a Dynamic Property[\s\S]*apicurio\.rest\.deletion\.group\.enabled`),
			},
			{
				Config:      propertyConfig("apicurio.rest.deletion.group.enabled", ""),
				ExpectError: regexp.MustCompile(`string length must be at least 1`),
			},
		},
	})
}

func TestAccConfigPropertyResource_serverRefusesValue(t *testing.T) {
	c := setupRegistry(t)
	if !serverAtLeast(t, c, 3, 1) {
		t.Skip("apicurio.log.level is dynamic from Apicurio Registry 3.1")
	}
	res := "apicurio_config_property.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// a string the provider cannot check; the registry refuses it at apply time
				Config:      propertyConfig("apicurio.log.level", "VERBOSE"),
				ExpectError: regexp.MustCompile(`Apicurio Registry Refused the Property Value[\s\S]*Invalid log level value`),
			},
			{
				Config: propertyConfig("apicurio.log.level", "INFO"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "type", "java.lang.String"),
					resource.TestCheckResourceAttr(res, "value", "INFO"),
				),
			},
		},
	})
}

func TestAccConfigPropertyResource_deletionSwitch(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-propswitch")
	cfg := func(value string) string {
		return propertyConfig(client.PropertyGroupDeletion, value) + fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id   = %q
  depends_on = [apicurio_config_property.test]
}
`, group)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("false"),
				Check:  resource.TestCheckResourceAttr("apicurio_config_property.test", "type", "java.lang.Boolean"),
			},
			{
				// the group cannot go while the switch is off; the error points at the resource
				Config:      cfg("false"),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`Group Deletion Is Disabled[\s\S]*apicurio_config_property`),
			},
			{
				Config: cfg("true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apicurio_config_property.test", plancheck.ResourceActionUpdate)},
				},
			},
			{Config: cfg("true"), Destroy: true},
		},
	})
}

func roleMappingConfig(principal, role, name string) string {
	nameLine := ""
	if name != "" {
		nameLine = fmt.Sprintf("  principal_name = %q\n", name)
	}
	return fmt.Sprintf(`
resource "apicurio_role_mapping" "test" {
  principal_id = %q
  role         = %q
%s}
`, principal, role, nameLine)
}

func TestAccRoleMappingResource_basic(t *testing.T) {
	c := setupRegistry(t)
	if !rbacAvailable(t, c) {
		t.Skip("the registry does not use application RBAC; role mappings are not available")
	}
	principal := randName("tfacc-user")
	res := "apicurio_role_mapping.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: roleMappingConfig(principal, "DEVELOPER", "Test User"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", principal),
					resource.TestCheckResourceAttr(res, "role", "DEVELOPER"),
					resource.TestCheckResourceAttr(res, "principal_name", "Test User"),
				),
			},
			{
				// the role changes in place
				Config: roleMappingConfig(principal, "ADMIN", "Test User"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(res, "role", "ADMIN"),
			},
			{
				// the name cannot: replacement
				Config: roleMappingConfig(principal, "ADMIN", "Renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionReplace)},
				},
				Check: resource.TestCheckResourceAttr(res, "principal_name", "Renamed"),
			},
			{
				// unsetting the name keeps what the registry has
				Config:   roleMappingConfig(principal, "ADMIN", ""),
				PlanOnly: true,
			},
			{
				// deleted outside Terraform: planned again
				PreConfig: func() {
					if err := c.DeleteRoleMapping(context.Background(), principal); err != nil {
						t.Fatalf("delete outside terraform: %v", err)
					}
				},
				Config: roleMappingConfig(principal, "READ_ONLY", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "role", "READ_ONLY"),
					resource.TestCheckNoResourceAttr(res, "principal_name"),
				),
			},
			{
				ResourceName:      res,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccRoleMappingResource_alreadyExists(t *testing.T) {
	c := setupRegistry(t)
	if !rbacAvailable(t, c) {
		t.Skip("the registry does not use application RBAC; role mappings are not available")
	}
	principal := randName("tfacc-mapped")
	ctx := context.Background()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					if err := c.CreateRoleMapping(ctx, client.RoleMapping{PrincipalID: principal, Role: client.RoleReadOnly}); err != nil {
						t.Fatalf("map outside terraform: %v", err)
					}
					t.Cleanup(func() { _ = c.DeleteRoleMapping(context.Background(), principal) })
				},
				Config:      roleMappingConfig(principal, "DEVELOPER", ""),
				ExpectError: regexp.MustCompile(`Already Exists[\s\S]*terraform import apicurio_role_mapping\.<name> ` + principal),
			},
			{
				Config:             roleMappingConfig(principal, "DEVELOPER", ""),
				ResourceName:       "apicurio_role_mapping.test",
				ImportState:        true,
				ImportStateId:      principal,
				ImportStatePersist: true,
			},
			{
				Config: roleMappingConfig(principal, "DEVELOPER", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apicurio_role_mapping.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr("apicurio_role_mapping.test", "role", "DEVELOPER"),
			},
		},
	})
}

func TestAccRoleMappingResource_validation(t *testing.T) {
	setupRegistry(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: roleMappingConfig("", "ADMIN", ""), ExpectError: regexp.MustCompile(`principal_id string length must be at least 1`)},
			{Config: roleMappingConfig("someone", "SUPERUSER", ""), ExpectError: regexp.MustCompile(`role value must be one of`)},
			{Config: roleMappingConfig("someone", "admin", ""), ExpectError: regexp.MustCompile(`role value must be one of`)},
		},
	})
}

func TestAccRoleMappingResource_rbacDisabled(t *testing.T) {
	c := setupRegistry(t)
	if liveServer() {
		if rbacAvailable(t, c) {
			t.Skip("the registry uses application RBAC; the 403 path cannot be exercised here")
		}
	} else {
		setDeletion(t, c, fakeRoleSourceProperty, "token")
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      roleMappingConfig("someone", "DEVELOPER", ""),
				ExpectError: regexp.MustCompile(`Role Mappings Are Not Available[\s\S]*role-source=application`),
			},
		},
	})
}

func TestAccCurrentUserDataSource(t *testing.T) {
	setupRegistry(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "apicurio_current_user" "me" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					// anonymous on an open registry, the admin service account on the secured one
					resource.TestCheckResourceAttr("data.apicurio_current_user.me", "admin", "true"),
					resource.TestCheckResourceAttrSet("data.apicurio_current_user.me", "id"),
					resource.TestCheckResourceAttr("data.apicurio_current_user.me", "viewer", "false"),
				),
			},
		},
	})
}
