package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

// versionBase renders a group and a content-less AVRO artifact; body is
// appended (version and branch resources).
func versionBase(group, body string) string {
	return fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id      = %q
  force_destroy = true
}

resource "apicurio_artifact" "test" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "price"
  artifact_type = "AVRO"
}
%s
`, group, body)
}

func versionBlock(name, body string) string {
	return fmt.Sprintf(`
resource "apicurio_artifact_version" %q {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
%s
}
`, name, body)
}

func TestAccArtifactVersionResource_basic(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-ver")
	var firstGlobalID string
	res := "apicurio_artifact_version.v1"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: versionBase(group, versionBlock("v1", fmt.Sprintf(`
  version = "1.0.0"
  name    = "first"
  labels  = { env = "prod" }
  content = %q`, avroPriceV1))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", group+"/price/1.0.0"),
					resource.TestCheckResourceAttr(res, "version", "1.0.0"),
					resource.TestCheckResourceAttr(res, "state", "ENABLED"),
					resource.TestCheckResourceAttr(res, "draft", "false"),
					resource.TestCheckResourceAttr(res, "name", "first"),
					resource.TestCheckResourceAttr(res, "labels.env", "prod"),
					resource.TestCheckResourceAttr(res, "artifact_type", "AVRO"),
					resource.TestCheckResourceAttr(res, "content_type", "application/json"),
					resource.TestCheckResourceAttrSet(res, "global_id"),
					func(s *terraformState) error {
						firstGlobalID = s.RootModule().Resources[res].Primary.Attributes["global_id"]
						return nil
					},
				),
			},
			{
				// metadata changes in place, same version and global id
				Config: versionBase(group, versionBlock("v1", fmt.Sprintf(`
  version     = "1.0.0"
  description = "described"
  content     = %q`, avroPriceV1))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(res, "name"),
					resource.TestCheckResourceAttr(res, "description", "described"),
					resource.TestCheckNoResourceAttr(res, "labels.%"),
					func(s *terraformState) error {
						if g := s.RootModule().Resources[res].Primary.Attributes["global_id"]; g != firstGlobalID {
							return fmt.Errorf("global_id changed on a metadata update: %s -> %s", firstGlobalID, g)
						}
						return nil
					},
				),
			},
			{
				// reformatted content: no change
				Config:   versionBase(group, versionBlock("v1", fmt.Sprintf("  version     = \"1.0.0\"\n  description = \"described\"\n  content     = %q", "{\n"+avroPriceV1[1:]))),
				PlanOnly: true,
			},
			{
				// content change replaces the version (same id, new global id)
				Config: versionBase(group, versionBlock("v1", fmt.Sprintf(`
  version     = "1.0.0"
  description = "described"
  content     = %q`, avroPriceV2))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "version", "1.0.0"),
					resource.TestCheckResourceAttr(res, "content", avroPriceV2),
					func(s *terraformState) error {
						if g := s.RootModule().Resources[res].Primary.Attributes["global_id"]; g == firstGlobalID {
							return fmt.Errorf("global_id did not change although the version was replaced")
						}
						return nil
					},
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

func TestAccArtifactVersionResource_serverNumbered(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-vernum")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: versionBase(group,
					versionBlock("a", fmt.Sprintf("  content = %q", avroPriceV1))+
						versionBlock("b", fmt.Sprintf("  content    = %q\n  depends_on = [apicurio_artifact_version.a]", avroPriceV2))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact_version.a", "version", "1"),
					resource.TestCheckResourceAttr("apicurio_artifact_version.b", "version", "2"),
				),
			},
			{
				Config: versionBase(group,
					versionBlock("a", fmt.Sprintf("  content = %q", avroPriceV1))+
						versionBlock("b", fmt.Sprintf("  content    = %q\n  depends_on = [apicurio_artifact_version.a]", avroPriceV2))),
				PlanOnly: true,
			},
		},
	})
}

func TestAccArtifactVersionResource_draft(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-draft")
	res := "apicurio_artifact_version.d"
	var globalID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: versionBase(group, versionBlock("d", fmt.Sprintf(`
  version = "draft-1"
  draft   = true
  content = %q`, avroPriceV1))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "state", "DRAFT"),
					resource.TestCheckResourceAttr(res, "draft", "true"),
					func(s *terraformState) error {
						globalID = s.RootModule().Resources[res].Primary.Attributes["global_id"]
						return nil
					},
				),
			},
			{
				// content of a draft changes in place: same global id
				Config: versionBase(group, versionBlock("d", fmt.Sprintf(`
  version = "draft-1"
  draft   = true
  content = %q`, avroPriceV2))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "state", "DRAFT"),
					resource.TestCheckResourceAttr(res, "content", avroPriceV2),
					func(s *terraformState) error {
						if g := s.RootModule().Resources[res].Primary.Attributes["global_id"]; g != globalID {
							return fmt.Errorf("a draft content update must keep the global id: %s -> %s", globalID, g)
						}
						return nil
					},
				),
			},
			{
				// promote
				Config: versionBase(group, versionBlock("d", fmt.Sprintf(`
  version = "draft-1"
  draft   = true
  state   = "ENABLED"
  content = %q`, avroPriceV2))),
				Check: resource.TestCheckResourceAttr(res, "state", "ENABLED"),
			},
			{
				ResourceName:            res,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"draft"},
			},
		},
	})
}

func TestAccArtifactVersionResource_states(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-states")
	res := "apicurio_artifact_version.v"
	cfg := func(state string) string {
		return versionBase(group, versionBlock("v", fmt.Sprintf("  version = \"1\"\n  state   = %q\n  content = %q", state, avroPriceV1)))
	}
	steps := []resource.TestStep{
		{Config: cfg("ENABLED"), Check: resource.TestCheckResourceAttr(res, "state", "ENABLED")},
		{Config: cfg("DEPRECATED"), Check: resource.TestCheckResourceAttr(res, "state", "DEPRECATED")},
		{
			// the registry hides the content of a disabled version; the last known one stays
			Config: cfg("DISABLED"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(res, "state", "DISABLED"),
				resource.TestCheckResourceAttr(res, "content", avroPriceV1),
			),
		},
		{Config: cfg("DISABLED"), PlanOnly: true},
		{Config: cfg("ENABLED"), Check: resource.TestCheckResourceAttr(res, "state", "ENABLED")},
	}
	if serverAtLeast(t, c, 3, 3) {
		steps = append(steps, resource.TestStep{Config: cfg("SUNSET"), Check: resource.TestCheckResourceAttr(res, "state", "SUNSET")})
	} else {
		steps = append(steps, resource.TestStep{Config: cfg("SUNSET"), ExpectError: regexp.MustCompile(`SUNSET Needs Apicurio Registry 3\.3`)})
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

func TestAccArtifactVersionResource_createDisabled(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-verdis")
	// created directly in a non-default state: the registry hides the content
	// of the disabled one, so content_type and references must be resolved
	// from the configuration
	cfg := versionBase(group,
		versionBlock("off", fmt.Sprintf("  version = \"1\"\n  state   = \"DISABLED\"\n  content = %q", avroPriceV1))+
			versionBlock("old", fmt.Sprintf("  version    = \"2\"\n  state      = \"DEPRECATED\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.off]", avroPriceV2)))
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact_version.off", "state", "DISABLED"),
					resource.TestCheckResourceAttr("apicurio_artifact_version.off", "content", avroPriceV1),
					resource.TestCheckResourceAttr("apicurio_artifact_version.off", "content_type", "application/json"),
					resource.TestCheckResourceAttr("apicurio_artifact_version.old", "state", "DEPRECATED"),
					resource.TestCheckResourceAttr("apicurio_artifact_version.old", "content_type", "application/json"),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

func TestAccArtifactVersionResource_importDisabled(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-verimp")
	ctx := context.Background()
	res := "apicurio_artifact_version.v"
	cfg := fmt.Sprintf(`
resource "apicurio_artifact_version" "v" {
  group_id    = %q
  artifact_id = "price"
  version     = "1"
  state       = "DISABLED"
  content     = %q
}
`, group, avroPriceV1)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					if _, err := c.CreateGroup(ctx, client.GroupCreate{GroupID: group}); err != nil {
						t.Fatalf("create group: %v", err)
					}
					t.Cleanup(func() { _ = c.DeleteGroup(context.Background(), group) })
					_, err := c.CreateArtifact(ctx, group, client.ArtifactCreate{ArtifactID: "price", ArtifactType: "AVRO",
						FirstVersion: &client.VersionCreate{Version: "1", Content: client.Content{Content: avroPriceV1, ContentType: "application/json"}}},
						client.CreateArtifactOptions{})
					if err != nil {
						t.Fatalf("create artifact: %v", err)
					}
					if err := c.UpdateVersionState(ctx, group, "price", "1", client.StateDisabled, false); err != nil {
						t.Fatalf("disable version: %v", err)
					}
				},
				Config:             cfg,
				ResourceName:       res,
				ImportState:        true,
				ImportStateId:      group + "/price/1",
				ImportStatePersist: true,
			},
			{
				// the registry hides the content of the disabled version: the
				// configured content is recorded, nothing is replaced
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "state", "DISABLED"),
					resource.TestCheckResourceAttr(res, "content", avroPriceV1),
					resource.TestCheckResourceAttr(res, "draft", "false"),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

func TestAccArtifactVersionResource_promotionRefused(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-promote")
	ctx := context.Background()
	// v1 enabled, then an incompatible draft, then v3 enabled (newer than the
	// draft) so that promoting the draft is checked against v3.
	base := versionBase(group,
		versionBlock("v1", fmt.Sprintf("  version = \"1\"\n  content = %q", avroPriceV1))+
			versionBlock("d", fmt.Sprintf("  version    = \"d\"\n  draft      = true\n  content    = %q\n  depends_on = [apicurio_artifact_version.v1]", avroPriceBad))+
			versionBlock("v3", fmt.Sprintf("  version    = \"3\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.d]", avroPriceV2)))
	promote := versionBase(group,
		versionBlock("v1", fmt.Sprintf("  version = \"1\"\n  content = %q", avroPriceV1))+
			versionBlock("d", fmt.Sprintf("  version    = \"d\"\n  draft      = true\n  state      = \"ENABLED\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.v1]", avroPriceBad))+
			versionBlock("v3", fmt.Sprintf("  version    = \"3\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.d]", avroPriceV2)))
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					// the rule is created on the artifact before the versions exist? no: the
					// artifact is created by Terraform, so the rule is set in the next step
				},
				Config: base,
				Check:  resource.TestCheckResourceAttr("apicurio_artifact_version.d", "state", "DRAFT"),
			},
			{
				PreConfig: func() {
					if err := c.CreateRule(ctx, client.RuleScope{GroupID: group, ArtifactID: "price"}, client.Rule{RuleType: client.RuleCompatibility, Config: "BACKWARD"}); err != nil {
						t.Fatalf("create rule: %v", err)
					}
				},
				Config:      promote,
				ExpectError: regexp.MustCompile(`Refused the State Transition[\s\S]*vat`),
			},
			{
				// still a draft, nothing else changed
				Config: base,
				Check:  resource.TestCheckResourceAttr("apicurio_artifact_version.d", "state", "DRAFT"),
			},
		},
	})
}

func TestAccArtifactVersionResource_disabledOperations(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-verlock")
	draft := func(content string) string {
		return versionBase(group, versionBlock("d", fmt.Sprintf("  version = \"d\"\n  draft   = true\n  content = %q", content)))
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: draft(avroPriceV1)},
			{
				PreConfig:   func() { setDeletion(t, c, client.PropertyContentMutability, "false") },
				Config:      draft(avroPriceV2),
				ExpectError: regexp.MustCompile(`Draft Content Updates Are Disabled[\s\S]*apicurio\.rest\.mutability\.artifact-version-content\.enabled`),
			},
			{
				PreConfig: func() {
					setDeletion(t, c, client.PropertyContentMutability, "true")
					setDeletion(t, c, client.PropertyVersionDeletion, "false")
				},
				Config:      draft(avroPriceV1),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`Version Deletion Is Disabled[\s\S]*apicurio\.rest\.deletion\.artifact-version\.enabled`),
			},
			{
				PreConfig: func() { setDeletion(t, c, client.PropertyVersionDeletion, "true") },
				Config:    draft(avroPriceV1),
				Destroy:   true,
			},
		},
	})
}

func TestAccArtifactVersionResource_validation(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-verval")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      versionBase(group, versionBlock("v", fmt.Sprintf("  state   = \"DRAFT\"\n  content = %q", avroPriceV1))),
				ExpectError: regexp.MustCompile(`state value must be one of`),
			},
			{
				Config:      versionBase(group, versionBlock("v", fmt.Sprintf("  version = \"bad id\"\n  content = %q", avroPriceV1))),
				ExpectError: regexp.MustCompile(`must consist of alphanumeric characters`),
			},
			{
				Config:      versionBase(group, versionBlock("v", `  content = ""`)),
				ExpectError: regexp.MustCompile(`Empty Content`),
			},
			{
				// the artifact must exist
				Config: fmt.Sprintf(`
resource "apicurio_artifact_version" "orphan" {
  group_id    = %q
  artifact_id = "nope"
  content     = %q
}`, group, avroPriceV1),
				ExpectError: regexp.MustCompile(`Apicurio Artifact Does Not Exist`),
			},
		},
	})
}

func TestAccArtifactVersionResource_alreadyExists(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-verdup")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: versionBase(group,
					versionBlock("a", fmt.Sprintf("  version = \"1\"\n  content = %q", avroPriceV1))+
						versionBlock("b", fmt.Sprintf("  version    = \"1\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.a]", avroPriceV2))),
				ExpectError: regexp.MustCompile(`Already Exists[\s\S]*terraform import`),
			},
		},
	})
}

// --------------------------------------------------------------------- branches

func branchConfig(group, body string) string {
	return versionBase(group,
		versionBlock("v1", fmt.Sprintf("  version = \"1\"\n  content = %q", avroPriceV1))+
			versionBlock("v2", fmt.Sprintf("  version    = \"2\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.v1]", avroPriceV2))+
			versionBlock("v3", fmt.Sprintf("  version    = \"3\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.v2]", avroPriceV2Fmt))+
			fmt.Sprintf(`
resource "apicurio_branch" "rel" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  branch_id   = "rel"
%s
}
`, body))
}

func TestAccBranchResource_basic(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-branch")
	res := "apicurio_branch.rel"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: branchConfig(group, `
  description = "release line"
  versions    = [apicurio_artifact_version.v3.version, apicurio_artifact_version.v1.version]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", group+"/price/rel"),
					resource.TestCheckResourceAttr(res, "description", "release line"),
					resource.TestCheckResourceAttr(res, "system_defined", "false"),
					resource.TestCheckResourceAttr(res, "versions.#", "2"),
					resource.TestCheckResourceAttr(res, "versions.0", "3"),
					resource.TestCheckResourceAttr(res, "versions.1", "1"),
				),
			},
			{
				Config: branchConfig(group, `
  versions = [apicurio_artifact_version.v1.version, apicurio_artifact_version.v2.version, apicurio_artifact_version.v3.version]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(res, "description"),
					resource.TestCheckResourceAttr(res, "versions.#", "3"),
					resource.TestCheckResourceAttr(res, "versions.0", "1"),
					resource.TestCheckResourceAttr(res, "versions.2", "3"),
				),
			},
			{
				// versions no longer managed: the registry's list stays, no diff
				Config: branchConfig(group, ``),
				Check:  resource.TestCheckResourceAttr(res, "versions.#", "3"),
			},
			{Config: branchConfig(group, ``), PlanOnly: true},
			{
				Config: branchConfig(group, `
  versions = []`),
				Check: resource.TestCheckResourceAttr(res, "versions.#", "0"),
			},
			{
				ResourceName:      res,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccBranchResource_validation(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-branchval")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: versionBase(group, `
resource "apicurio_branch" "latest" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  branch_id   = "latest"
}`),
				ExpectError: regexp.MustCompile(`branch_id value must be none of`),
			},
			{
				Config:      branchConfig(group, `  versions = ["1", "1"]`),
				ExpectError: regexp.MustCompile(`Duplicate Version`),
			},
			{
				Config:      branchConfig(group, `  versions = ["1", "42"]`),
				ExpectError: regexp.MustCompile(`Unknown Versions[\s\S]*42`),
			},
		},
	})
}

func TestAccBranchResource_disappears(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-branchgone")
	cfg := branchConfig(group, `  versions = [apicurio_artifact_version.v1.version]`)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					if err := c.DeleteBranch(context.Background(), group, "price", "rel"); err != nil {
						t.Fatalf("delete outside terraform: %v", err)
					}
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apicurio_branch.rel", plancheck.ResourceActionCreate)},
				},
				Check: resource.TestCheckResourceAttr("apicurio_branch.rel", "versions.#", "1"),
			},
		},
	})
}

// --------------------------------------------------------------------- data source

func TestAccArtifactVersionDataSource(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-verds")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: versionBase(group,
					versionBlock("v1", fmt.Sprintf("  version = \"1\"\n  name    = \"one\"\n  content = %q", avroPriceV1))+
						versionBlock("v2", fmt.Sprintf("  version    = \"2\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.v1]", avroPriceV2))) + `
data "apicurio_artifact_version" "by_id" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  version     = apicurio_artifact_version.v1.version
}

data "apicurio_artifact_version" "latest" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  depends_on  = [apicurio_artifact_version.v2]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.by_id", "resolved_version", "1"),
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.by_id", "name", "one"),
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.by_id", "state", "ENABLED"),
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.by_id", "content", avroPriceV1),
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.by_id", "content_type", "application/json"),
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.by_id", "id", group+"/price/1"),
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.latest", "resolved_version", "2"),
					resource.TestCheckResourceAttr("data.apicurio_artifact_version.latest", "content", avroPriceV2),
				),
			},
			{
				Config: fmt.Sprintf(`data "apicurio_artifact_version" "missing" {
  group_id    = %q
  artifact_id = "price"
  version     = "99"
}`, group),
				ExpectError: regexp.MustCompile(`Apicurio Artifact Version Not Found`),
			},
		},
	})
}
