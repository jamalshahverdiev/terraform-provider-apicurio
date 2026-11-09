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

// brokenAvro is valid JSON but not an Avro schema (a record without fields):
// any VALIDITY rule (SYNTAX_ONLY or FULL) refuses it, NONE or no rule accept it.
const brokenAvro = `{"type":"record","name":"Broken"}`

func globalRule(ruleType, config string) string {
	return fmt.Sprintf(`
resource "apicurio_global_rule" "test" {
  rule_type = %q
  config    = %q
}
`, ruleType, config)
}

func TestAccGlobalRuleResource_basic(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-grule")
	res := "apicurio_global_rule.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: globalRule("VALIDITY", "SYNTAX_ONLY"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", "VALIDITY"),
					resource.TestCheckResourceAttr(res, "rule_type", "VALIDITY"),
					resource.TestCheckResourceAttr(res, "config", "SYNTAX_ONLY"),
				),
			},
			{
				// config changes in place
				Config: globalRule("VALIDITY", "FULL"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(res, "config", "FULL"),
			},
			{
				// the global rule applies to every artifact without its own rule
				Config: globalRule("VALIDITY", "FULL") + versionBase(group,
					versionBlock("bad", fmt.Sprintf("  content    = %q\n  depends_on = [apicurio_global_rule.test]", brokenAvro))),
				ExpectError: regexp.MustCompile(`Refused the Version[\s\S]*(Syntax violation|violation)`),
			},
			{
				// an artifact rule set to NONE switches the type off for that artifact
				Config: globalRule("VALIDITY", "FULL") + versionBase(group, `
resource "apicurio_artifact_rule" "off" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  rule_type   = "VALIDITY"
  config      = "NONE"
}
`+versionBlock("bad", fmt.Sprintf("  content    = %q\n  depends_on = [apicurio_global_rule.test, apicurio_artifact_rule.off]", brokenAvro))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact_rule.off", "id", group+"/price/VALIDITY"),
					resource.TestCheckResourceAttr("apicurio_artifact_version.bad", "content", brokenAvro),
				),
			},
			{
				// rule_type change replaces the rule
				Config: globalRule("INTEGRITY", "REFS_EXIST,NO_DUPLICATES"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", "INTEGRITY"),
					resource.TestCheckResourceAttr(res, "config", "REFS_EXIST,NO_DUPLICATES"),
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

func groupRule(group, config string) string {
	return fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id      = %q
  force_destroy = true
}

resource "apicurio_group_rule" "test" {
  group_id  = apicurio_group.test.group_id
  rule_type = "VALIDITY"
  config    = %q
}
`, group, config)
}

func TestAccGroupRuleResource_basic(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-grprule")
	res := "apicurio_group_rule.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: groupRule(group, "FULL"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", group+"/VALIDITY"),
					resource.TestCheckResourceAttr(res, "group_id", group),
					resource.TestCheckResourceAttr(res, "config", "FULL"),
				),
			},
			{
				Config: groupRule(group, "SYNTAX_ONLY"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(res, "config", "SYNTAX_ONLY"),
			},
			{
				// the group rule applies to the artifacts of the group (set back
				// to FULL in the same apply; the artifact depends on the rule)
				Config: groupRule(group, "FULL") + fmt.Sprintf(`
resource "apicurio_artifact" "test" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "price"
  artifact_type = "AVRO"
  content       = %q
  depends_on    = [apicurio_group_rule.test]
}
`, brokenAvro),
				ExpectError: regexp.MustCompile(`violation`),
			},
			{
				// deleted outside Terraform: planned again as a creation
				PreConfig: func() {
					if err := c.DeleteRule(context.Background(), client.RuleScope{GroupID: group}, client.RuleValidity); err != nil {
						t.Fatalf("delete rule outside terraform: %v", err)
					}
				},
				Config: groupRule(group, "SYNTAX_ONLY"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionCreate)},
				},
			},
			{
				ResourceName:      res,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func artifactRule(group, config, extra string) string {
	return versionBase(group, fmt.Sprintf(`
resource "apicurio_artifact_rule" "compat" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  rule_type   = "COMPATIBILITY"
  config      = %q
}
%s`, config, extra))
}

func TestAccArtifactRuleResource_basic(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-arule")
	res := "apicurio_artifact_rule.compat"
	v1 := versionBlock("v1", fmt.Sprintf("  version    = \"1\"\n  content    = %q\n  depends_on = [apicurio_artifact_rule.compat]", avroPriceV1))
	v2 := versionBlock("v2", fmt.Sprintf("  version    = \"2\"\n  content    = %q\n  depends_on = [apicurio_artifact_version.v1]", avroPriceBad))
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: artifactRule(group, "BACKWARD", v1),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "id", group+"/price/COMPATIBILITY"),
					resource.TestCheckResourceAttr(res, "rule_type", "COMPATIBILITY"),
					resource.TestCheckResourceAttr(res, "config", "BACKWARD"),
				),
			},
			{
				// the rule refuses an incompatible version, with the cause
				Config:      artifactRule(group, "BACKWARD", v1+v2),
				ExpectError: regexp.MustCompile(`Refused the Version[\s\S]*vat`),
			},
			{
				// NONE switches the check off; the version is created
				Config: artifactRule(group, "NONE", v1+v2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(res, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(res, "config", "NONE"),
					resource.TestCheckResourceAttr("apicurio_artifact_version.v2", "version", "2"),
				),
			},
			{Config: artifactRule(group, "NONE", v1+v2), PlanOnly: true},
			{
				ResourceName:      res,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccRuleResource_validation(t *testing.T) {
	setupRegistry(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: globalRule("BOGUS", "FULL"), ExpectError: regexp.MustCompile(`rule_type value must be one of`)},
			{Config: globalRule("VALIDITY", "BACKWARD"), ExpectError: regexp.MustCompile(`VALIDITY rules take one of[\s\S]*got\s+"BACKWARD"`)},
			{Config: globalRule("VALIDITY", "full"), ExpectError: regexp.MustCompile(`Invalid Rule Configuration`)},
			{Config: globalRule("COMPATIBILITY", "SIDEWAYS"), ExpectError: regexp.MustCompile(`COMPATIBILITY rules take one of`)},
			{Config: globalRule("INTEGRITY", "REFS_EXIST,BOGUS"), ExpectError: regexp.MustCompile(`INTEGRITY rules take a comma-separated set[\s\S]*got\s+"BOGUS"`)},
			{Config: globalRule("INTEGRITY", "REFS_EXIST,REFS_EXIST"), ExpectError: regexp.MustCompile(`listed more than once`)},
			{Config: globalRule("INTEGRITY", "FULL,REFS_EXIST"), ExpectError: regexp.MustCompile(`stand alone`)},
			{Config: globalRule("INTEGRITY", "REFS_EXIST,"), ExpectError: regexp.MustCompile(`without empty entries`)},
			{Config: globalRule("VALIDITY", ""), ExpectError: regexp.MustCompile(`VALIDITY rules take one of[\s\S]*got\s+""`)},
			{Config: globalRule("INTEGRITY", ""), ExpectError: regexp.MustCompile(`without empty entries`)},
			{
				Config: `
resource "apicurio_group_rule" "orphan" {
  group_id  = "tfacc-no-such-group"
  rule_type = "VALIDITY"
  config    = "FULL"
}`,
				ExpectError: regexp.MustCompile(`Apicurio Group Does Not Exist`),
			},
			{
				Config: `
resource "apicurio_artifact_rule" "orphan" {
  group_id    = "tfacc-no-such-group"
  artifact_id = "nope"
  rule_type   = "VALIDITY"
  config      = "FULL"
}`,
				ExpectError: regexp.MustCompile(`Apicurio Artifact Does Not Exist`),
			},
		},
	})
}

func TestAccArtifactRuleResource_integrity(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-integ")
	rule := func(config string) string {
		return versionBase(group, fmt.Sprintf(`
resource "apicurio_artifact_rule" "integrity" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  rule_type   = "INTEGRITY"
  config      = %q
}
`, config)+versionBlock("v1", fmt.Sprintf(`
  version    = "1"
  content    = %q
  references = [{ group_id = %q, artifact_id = "missing", version = "1", name = "tfacc.Missing" }]
  depends_on = [apicurio_artifact_rule.integrity]`, avroPriceV1, group)))
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// a reference to a missing artifact is refused by REFS_EXIST
				Config:      rule("REFS_EXIST,NO_DUPLICATES"),
				ExpectError: regexp.MustCompile(`Refused the Version[\s\S]*Referenced artifact`),
			},
			{
				Config: rule("NONE"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact_rule.integrity", "config", "NONE"),
					resource.TestCheckResourceAttr("apicurio_artifact_version.v1", "references.#", "1"),
				),
			},
		},
	})
}

func TestAccGroupRuleResource_addedAfterVersion(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-rulelate")
	existing := versionBase(group, versionBlock("v1", fmt.Sprintf("  version = \"1\"\n  content = %q", brokenAvro)))
	withRule := existing + `
resource "apicurio_group_rule" "validity" {
  group_id  = apicurio_group.test.group_id
  rule_type = "VALIDITY"
  config    = "FULL"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// without a rule the registry stores content it cannot parse
				Config: existing,
				Check:  resource.TestCheckResourceAttr("apicurio_artifact_version.v1", "content", brokenAvro),
			},
			{
				// a rule added later leaves the existing version alone
				Config: withRule,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("apicurio_artifact_version.v1", plancheck.ResourceActionNoop)},
				},
			},
			{Config: withRule, PlanOnly: true},
		},
	})
}

func TestAccGroupRuleResource_groupDisappears(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-rulegrpgone")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupRule(group, "FULL")},
			{
				PreConfig: func() {
					if err := c.DeleteGroup(context.Background(), group); err != nil {
						t.Fatalf("delete group outside terraform: %v", err)
					}
				},
				Config: groupRule(group, "FULL"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("apicurio_group.test", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction("apicurio_group_rule.test", plancheck.ResourceActionCreate),
					},
				},
				Check: resource.TestCheckResourceAttr("apicurio_group_rule.test", "config", "FULL"),
			},
		},
	})
}

func TestAccRuleResource_importErrors(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-ruleimp")
	res := "apicurio_group_rule.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: groupRule(group, "FULL")},
			{
				Config:        groupRule(group, "FULL"),
				ResourceName:  res,
				ImportState:   true,
				ImportStateId: group + "/BOGUS",
				ExpectError:   regexp.MustCompile(`Invalid Import ID`),
			},
			{
				Config:        groupRule(group, "FULL"),
				ResourceName:  res,
				ImportState:   true,
				ImportStateId: "VALIDITY",
				ExpectError:   regexp.MustCompile(`Invalid Import ID`),
			},
			{
				// a rule that is not configured cannot be imported
				Config:        groupRule(group, "FULL"),
				ResourceName:  res,
				ImportState:   true,
				ImportStateId: group + "/COMPATIBILITY",
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
		},
	})
}

func TestAccArtifactRuleResource_slashInArtifactID(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-ruleslash")
	cfg := fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id      = %q
  force_destroy = true
}

resource "apicurio_artifact" "test" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "orders/price"
  artifact_type = "AVRO"
}

resource "apicurio_artifact_rule" "validity" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  rule_type   = "VALIDITY"
  config      = "FULL"
}
`, group)
	res := "apicurio_artifact_rule.validity"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check:  resource.TestCheckResourceAttr(res, "id", group+"/orders/price/VALIDITY"),
			},
			{
				ResourceName:      res,
				ImportState:       true,
				ImportStateId:     group + "/orders/price/VALIDITY",
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccArtifactRuleResource_alreadyExists(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-aruledup")
	ctx := context.Background()
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: versionBase(group, "")},
			{
				PreConfig: func() {
					if err := c.CreateRule(ctx, client.RuleScope{GroupID: group, ArtifactID: "price"}, client.Rule{RuleType: client.RuleValidity, Config: "FULL"}); err != nil {
						t.Fatalf("create rule outside terraform: %v", err)
					}
				},
				Config: versionBase(group, `
resource "apicurio_artifact_rule" "dup" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  rule_type   = "VALIDITY"
  config      = "FULL"
}`),
				ExpectError: regexp.MustCompile(`Apicurio Artifact Rule Already Exists[\s\S]*terraform import apicurio_artifact_rule\.<name> ` + group + `/price/VALIDITY`),
			},
			{
				// imported, it is managed from then on
				Config: versionBase(group, `
resource "apicurio_artifact_rule" "dup" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  rule_type   = "VALIDITY"
  config      = "SYNTAX_ONLY"
}`),
				ResourceName:       "apicurio_artifact_rule.dup",
				ImportState:        true,
				ImportStateId:      group + "/price/VALIDITY",
				ImportStatePersist: true,
			},
			{
				Config: versionBase(group, `
resource "apicurio_artifact_rule" "dup" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
  rule_type   = "VALIDITY"
  config      = "SYNTAX_ONLY"
}`),
				Check: resource.TestCheckResourceAttr("apicurio_artifact_rule.dup", "config", "SYNTAX_ONLY"),
			},
		},
	})
}
