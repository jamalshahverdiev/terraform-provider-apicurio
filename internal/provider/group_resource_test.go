package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

func groupConfig(id, body string) string {
	return fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id = %q
%s
}
`, id, body)
}

func TestAccGroupResource_basic(t *testing.T) {
	setupRegistry(t)
	id := randName("tfacc-group")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: groupConfig(id, `
  description = "Payments schemas"
  labels = {
    env  = "prod"
    team = "payments"
  }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_group.test", "id", id),
					resource.TestCheckResourceAttr("apicurio_group.test", "group_id", id),
					resource.TestCheckResourceAttr("apicurio_group.test", "description", "Payments schemas"),
					resource.TestCheckResourceAttr("apicurio_group.test", "labels.%", "2"),
					resource.TestCheckResourceAttr("apicurio_group.test", "labels.env", "prod"),
					resource.TestCheckResourceAttr("apicurio_group.test", "force_destroy", "false"),
					resource.TestCheckResourceAttrSet("apicurio_group.test", "created_on"),
					resource.TestCheckResourceAttrSet("apicurio_group.test", "modified_on"),
				),
			},
			{
				// update in place: description cleared, labels replaced
				Config: groupConfig(id, `
  labels = {
    env = "staging"
  }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("apicurio_group.test", "description"),
					resource.TestCheckResourceAttr("apicurio_group.test", "labels.%", "1"),
					resource.TestCheckResourceAttr("apicurio_group.test", "labels.env", "staging"),
				),
			},
			{
				// labels removed entirely
				Config: groupConfig(id, ``),
				Check:  resource.TestCheckNoResourceAttr("apicurio_group.test", "labels.%"),
			},
			{
				ResourceName:            "apicurio_group.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"force_destroy"},
			},
		},
	})
}

func TestAccGroupResource_reservedID(t *testing.T) {
	setupRegistry(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupConfig("default", ""), ExpectError: regexp.MustCompile(`value must be none of: \["default"\]`)},
			{Config: groupConfig("a/b", ""), ExpectError: regexp.MustCompile(`must not contain a slash`)},
		},
	})
}

func TestAccGroupResource_owner(t *testing.T) {
	c := setupRegistry(t)
	id := randName("tfacc-owner")
	if !serverAtLeast(t, c, 3, 1) {
		// 3.0.x accepts the owner in the group update and drops it.
		resource.Test(t, resource.TestCase{
			PreCheck:                 func() { testAccPreCheck(t) },
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{Config: groupConfig(id, `owner = "alice"`), ExpectError: regexp.MustCompile(`Owner Not Stored by This Registry`)},
				{Config: groupConfig(id, ``)},
			},
		})
		return
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: groupConfig(id, `owner = "alice"`),
				Check:  resource.TestCheckResourceAttr("apicurio_group.test", "owner", "alice"),
			},
			{
				Config: groupConfig(id, `owner = "bob"`),
				Check:  resource.TestCheckResourceAttr("apicurio_group.test", "owner", "bob"),
			},
			{
				// removed from the configuration: the server keeps it, no diff
				Config: groupConfig(id, ``),
				Check:  resource.TestCheckResourceAttr("apicurio_group.test", "owner", "bob"),
			},
			{Config: groupConfig(id, ``), PlanOnly: true},
			{
				ResourceName:            "apicurio_group.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"force_destroy"},
			},
		},
	})
}

func TestAccGroupResource_alreadyExists(t *testing.T) {
	c := setupRegistry(t)
	id := randName("tfacc-existing")
	ctx := context.Background()
	if _, err := c.CreateGroup(ctx, client.GroupCreate{GroupID: id}); err != nil {
		t.Fatalf("precreate: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteGroup(ctx, id) })
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupConfig(id, ""), ExpectError: regexp.MustCompile(`already exists[\s\S]*terraform import`)},
		},
	})
}

func TestAccGroupResource_disappears(t *testing.T) {
	c := setupRegistry(t)
	id := randName("tfacc-gone")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupConfig(id, `description = "first"`)},
			{
				PreConfig: func() {
					if err := c.DeleteGroup(context.Background(), id); err != nil {
						t.Fatalf("delete outside terraform: %v", err)
					}
				},
				Config: groupConfig(id, `description = "first"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_group.test", "description", "first"),
					func(*terraformState) error {
						if _, err := c.GetGroup(context.Background(), id); err != nil {
							return fmt.Errorf("group was not recreated: %w", err)
						}
						return nil
					},
				),
			},
		},
	})
}

func TestAccGroupResource_forceDestroy(t *testing.T) {
	c := setupRegistry(t)
	id := randName("tfacc-force")
	ctx := context.Background()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupConfig(id, "")},
			{
				// an artifact appears in the group outside Terraform
				PreConfig: func() {
					_, err := c.CreateArtifact(ctx, id, client.ArtifactCreate{ArtifactID: "outside", ArtifactType: "AVRO",
						FirstVersion: &client.VersionCreate{Content: client.Content{Content: avroPriceV1, ContentType: "application/json"}}}, client.CreateArtifactOptions{})
					if err != nil {
						t.Fatalf("create artifact outside terraform: %v", err)
					}
				},
				Config:      groupConfig(id, ""),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`Still Contains Artifacts[\s\S]*outside[\s\S]*force_destroy\s*=\s*true`),
			},
			{
				Config: groupConfig(id, `force_destroy = true`),
				Check:  resource.TestCheckResourceAttr("apicurio_group.test", "force_destroy", "true"),
			},
			{
				Config:  groupConfig(id, `force_destroy = true`),
				Destroy: true,
				Check: func(*terraformState) error {
					_, err := c.GetGroup(ctx, id)
					switch {
					case err == nil:
						return fmt.Errorf("group %q should be gone but still exists", id)
					case !client.IsNotFound(err):
						return fmt.Errorf("unexpected error reading the deleted group: %w", err)
					}
					return nil
				},
			},
		},
	})
}

func TestAccGroupResource_deletionDisabled(t *testing.T) {
	c := setupRegistry(t)
	id := randName("tfacc-locked")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupConfig(id, "")},
			{
				PreConfig:   func() { setDeletion(t, c, client.PropertyGroupDeletion, "false") },
				Config:      groupConfig(id, ""),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`Group Deletion Is Disabled[\s\S]*apicurio\.rest\.deletion\.group\.enabled[\s\S]*apicurio_config_property`),
			},
			{
				PreConfig: func() { setDeletion(t, c, client.PropertyGroupDeletion, "true") },
				Config:    groupConfig(id, ""),
				Destroy:   true,
			},
		},
	})
}

func TestAccGroupDataSource(t *testing.T) {
	setupRegistry(t)
	id := randName("tfacc-ds-group")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: groupConfig(id, `
  description = "looked up"
  labels = { env = "prod" }`) + `
data "apicurio_group" "test" {
  group_id = apicurio_group.test.group_id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apicurio_group.test", "id", id),
					resource.TestCheckResourceAttr("data.apicurio_group.test", "description", "looked up"),
					resource.TestCheckResourceAttr("data.apicurio_group.test", "labels.env", "prod"),
					resource.TestCheckResourceAttrSet("data.apicurio_group.test", "created_on"),
				),
			},
			{
				Config: `data "apicurio_group" "missing" {
  group_id = "tfacc-does-not-exist"
}`,
				ExpectError: regexp.MustCompile(`Apicurio Group Not Found`),
			},
		},
	})
}
