package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/jamalshahverdiev/terraform-provider-apicurio/internal/client"
)

type terraformState = terraform.State

// artifactConfig renders a group and an artifact in it; body is the inner
// HCL of the artifact.
func artifactConfig(group, artifact, body string) string {
	return fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id      = %q
  force_destroy = true
}

resource "apicurio_artifact" "test" {
  group_id    = apicurio_group.test.group_id
  artifact_id = %q
%s
}
`, group, artifact, body)
}

func TestAccArtifactResource_metadataOnly(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-art")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: artifactConfig(group, "prices", `
  artifact_type = "AVRO"
  name          = "Prices"
  description   = "Price events"
  labels = {
    env = "prod"
  }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "id", group+"/prices"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "artifact_type", "AVRO"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "name", "Prices"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "description", "Price events"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "labels.env", "prod"),
					resource.TestCheckNoResourceAttr("apicurio_artifact.test", "version"),
					resource.TestCheckNoResourceAttr("apicurio_artifact.test", "content"),
					resource.TestCheckResourceAttrSet("apicurio_artifact.test", "created_on"),
				),
			},
			{
				Config: artifactConfig(group, "prices", `
  artifact_type = "AVRO"
  name          = "Prices v2"
  owner         = "alice"
  labels = {
    env  = "prod"
    team = "payments"
  }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "name", "Prices v2"),
					resource.TestCheckNoResourceAttr("apicurio_artifact.test", "description"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "labels.%", "2"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "owner", "alice"),
				),
			},
			{
				// owner removed from the configuration: kept, no diff
				Config: artifactConfig(group, "prices", `
  artifact_type = "AVRO"
  name          = "Prices v2"
  labels = {
    env  = "prod"
    team = "payments"
  }`),
				Check: resource.TestCheckResourceAttr("apicurio_artifact.test", "owner", "alice"),
			},
			{
				ResourceName:      "apicurio_artifact.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccArtifactResource_content(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-content")
	var firstGlobalID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: artifactConfig(group, "price", fmt.Sprintf("  content = %q", avroPriceV1)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "artifact_type", "AVRO"), // detected
					resource.TestCheckResourceAttr("apicurio_artifact.test", "content_type", "application/json"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "1"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "content", avroPriceV1),
					resource.TestCheckResourceAttrSet("apicurio_artifact.test", "global_id"),
					resource.TestCheckResourceAttrSet("apicurio_artifact.test", "content_id"),
					resource.TestCheckNoResourceAttr("apicurio_artifact.test", "references.#"),
					func(s *terraformState) error {
						firstGlobalID = s.RootModule().Resources["apicurio_artifact.test"].Primary.Attributes["global_id"]
						return nil
					},
				),
			},
			{
				// a compatible change creates version 2
				Config: artifactConfig(group, "price", fmt.Sprintf("  content = %q", avroPriceV2)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "2"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "content", avroPriceV2),
					func(s *terraformState) error {
						if g := s.RootModule().Resources["apicurio_artifact.test"].Primary.Attributes["global_id"]; g == firstGlobalID {
							return fmt.Errorf("global_id did not change with the new version: %s", g)
						}
						return nil
					},
				),
			},
			{
				// the same document, pretty-printed: no change planned
				Config:   artifactConfig(group, "price", fmt.Sprintf("  content = %q", avroPriceV2Fmt)),
				PlanOnly: true,
			},
			{
				// a version created outside Terraform shows up and is superseded
				PreConfig: func() {
					_, err := c.CreateVersion(context.Background(), group, "price", client.VersionCreate{Content: client.Content{Content: avroPriceV1, ContentType: "application/json"}}, false)
					if err != nil {
						t.Fatalf("create version outside terraform: %v", err)
					}
				},
				Config: artifactConfig(group, "price", fmt.Sprintf("  content = %q", avroPriceV2)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "4"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "content", avroPriceV2),
				),
			},
			{
				ResourceName:      "apicurio_artifact.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// only the media type changes: no new version, the configured
				// value is kept (the registry reports the first upload's type)
				Config: artifactConfig(group, "price", fmt.Sprintf("  content      = %q\n  content_type = \"application/vnd.tfacc+json\"", avroPriceV2)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "4"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "content_type", "application/vnd.tfacc+json"),
				),
			},
			{
				Config:   artifactConfig(group, "price", fmt.Sprintf("  content      = %q\n  content_type = \"application/vnd.tfacc+json\"", avroPriceV2)),
				PlanOnly: true,
			},
		},
	})
}

func TestAccArtifactResource_explicitTypes(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-types")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: artifactConfig(group, "probe.proto", fmt.Sprintf(`
  artifact_type = "PROTOBUF"
  content       = %q`, protoSchema)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "artifact_type", "PROTOBUF"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "content_type", "application/x-protobuf"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "1"),
				),
			},
			{
				// an explicit content type is kept as configured even when the
				// server reports it with parameters
				Config: artifactConfig(group, "probe.proto", fmt.Sprintf(`
  artifact_type = "PROTOBUF"
  content_type  = "text/plain"
  content       = %q`, protoSchema+"// comment\n")),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "content_type", "text/plain"),
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "2"),
				),
			},
		},
	})
}

// referencesConfig renders price + tax artifacts and an order artifact that
// references them; refs is the HCL of the references attribute ("" omits it).
func referencesConfig(group, content, refs string) string {
	return fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id      = %q
  force_destroy = true
}

resource "apicurio_artifact" "price" {
  group_id    = apicurio_group.test.group_id
  artifact_id = "price"
  content     = %q
}

resource "apicurio_artifact" "tax" {
  group_id    = apicurio_group.test.group_id
  artifact_id = "tax"
  content     = %q
}

resource "apicurio_artifact" "order" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "order"
  artifact_type = "AVRO" # the registry cannot detect the type of content whose named types live elsewhere
  content       = %q
%s
}
`, group, avroPriceV1, avroTax, content, refs)
}

const (
	refPrice = `{
    group_id    = apicurio_artifact.price.group_id
    artifact_id = apicurio_artifact.price.artifact_id
    version     = apicurio_artifact.price.version
    name        = "tfacc.Price"
  }`
	refTax = `{
    group_id    = apicurio_artifact.tax.group_id
    artifact_id = apicurio_artifact.tax.artifact_id
    version     = apicurio_artifact.tax.version
    name        = "tfacc.Tax"
  }`
)

func TestAccArtifactResource_references(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-refs")
	priceTax := "  references = [" + refPrice + ", " + refTax + "]"
	taxPrice := "  references = [" + refTax + ", " + refPrice + "]"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: referencesConfig(group, avroOrder2, priceTax),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.order", "artifact_type", "AVRO"),
					resource.TestCheckResourceAttr("apicurio_artifact.order", "version", "1"),
					resource.TestCheckResourceAttr("apicurio_artifact.order", "references.#", "2"),
					resource.TestCheckResourceAttr("apicurio_artifact.order", "references.0.artifact_id", "price"),
					resource.TestCheckResourceAttr("apicurio_artifact.order", "references.0.version", "1"),
					resource.TestCheckResourceAttr("apicurio_artifact.order", "references.0.name", "tfacc.Price"),
					resource.TestCheckResourceAttr("apicurio_artifact.order", "references.1.name", "tfacc.Tax"),
				),
			},
			{Config: referencesConfig(group, avroOrder2, priceTax), PlanOnly: true},
			// the same references in another order: not a change
			{Config: referencesConfig(group, avroOrder2, taxPrice), PlanOnly: true},
			{
				Config: referencesConfig(group, avroOrder2, taxPrice),
				Check:  resource.TestCheckResourceAttr("apicurio_artifact.order", "version", "1"),
			},
			{
				// references dropped from the configuration and the content
				// changed: the new version keeps the stored references
				Config: referencesConfig(group, avroOrder2V2, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.order", "version", "2"),
					resource.TestCheckResourceAttr("apicurio_artifact.order", "references.#", "2"),
				),
			},
			{Config: referencesConfig(group, avroOrder2V2, ""), PlanOnly: true},
			{
				ResourceName:      "apicurio_artifact.order",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccArtifactResource_ruleViolation(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-rules")
	ctx := context.Background()
	scope := client.RuleScope{GroupID: group, ArtifactID: "price"}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: artifactConfig(group, "price", fmt.Sprintf("  content = %q", avroPriceV1))},
			{
				PreConfig: func() {
					if err := c.CreateRule(ctx, scope, client.Rule{RuleType: client.RuleCompatibility, Config: "BACKWARD"}); err != nil {
						t.Fatalf("create rule: %v", err)
					}
				},
				Config:      artifactConfig(group, "price", fmt.Sprintf("  content = %q", avroPriceBad)),
				ExpectError: regexp.MustCompile(`Refused the New Version[\s\S]*Incompatible[\s\S]*vat\s+at\s+/fields/3`),
			},
			{
				// the stored version is still 1 and the compatible content is accepted
				Config: artifactConfig(group, "price", fmt.Sprintf("  content = %q", avroPriceV2)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "2"),
				),
			},
		},
	})
}

func TestAccArtifactResource_validation(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-invalid")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      artifactConfig(group, "x", `  artifact_type = "COBOL"`),
				ExpectError: regexp.MustCompile(`does not know the artifact\s+type\s+"COBOL"`),
			},
			{
				Config:      artifactConfig(group, "x", ``),
				ExpectError: regexp.MustCompile(`Artifact Type Required`),
			},
			{
				Config: artifactConfig(group, "x", `
  artifact_type = "AVRO"
  content       = "{not json"`),
				ExpectError: regexp.MustCompile(`must be valid JSON`),
			},
			{
				Config: artifactConfig(group, "x", `
  references = [{ group_id = "g", artifact_id = "a", version = "1", name = "n" }]`),
				ExpectError: regexp.MustCompile(`References Without Content`),
			},
			{
				Config:      artifactConfig(group, "x", `  content = ""`),
				ExpectError: regexp.MustCompile(`Empty Content`),
			},
			{
				// last: this configuration passes validation, so the post-test destroy can run
				Config:      artifactConfig(group, "x", `  content = "just some words"`),
				ExpectError: regexp.MustCompile(`Could Not Detect the Artifact\s+Type`),
			},
		},
	})
}

func TestAccArtifactResource_autoGroupCreationOff(t *testing.T) {
	setupRegistry(t)
	if fake == nil {
		t.Skip("needs the fake registry (the live stack has automatic group creation on)")
	}
	fake.autoGroup = false
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "apicurio_artifact" "test" {
  group_id      = "tfacc-no-group"
  artifact_id   = "x"
  artifact_type = "AVRO"
}`,
				ExpectError: regexp.MustCompile(`does not create\s+groups\s+automatically[\s\S]*apicurio_group`),
			},
		},
	})
}

func TestAccArtifactResource_deletionDisabled(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-artlock")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: artifactConfig(group, "x", `  artifact_type = "AVRO"`)},
			{
				PreConfig:   func() { setDeletion(t, c, client.PropertyArtifactDeletion, "false") },
				Config:      artifactConfig(group, "x", `  artifact_type = "AVRO"`),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`Artifact Deletion Is Disabled[\s\S]*apicurio\.rest\.deletion\.artifact\.enabled`),
			},
			{
				PreConfig: func() { setDeletion(t, c, client.PropertyArtifactDeletion, "true") },
				Config:    artifactConfig(group, "x", `  artifact_type = "AVRO"`),
				Destroy:   true,
			},
		},
	})
}

func TestAccArtifactResource_disappears(t *testing.T) {
	c := setupRegistry(t)
	group := randName("tfacc-artgone")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: artifactConfig(group, "x", fmt.Sprintf("  content = %q", avroPriceV1))},
			{
				PreConfig: func() {
					if err := c.DeleteArtifact(context.Background(), group, "x"); err != nil {
						t.Fatalf("delete outside terraform: %v", err)
					}
				},
				Config: artifactConfig(group, "x", fmt.Sprintf("  content = %q", avroPriceV1)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("apicurio_artifact.test", "version", "1"),
				),
			},
		},
	})
}

func TestAccArtifactDataSource(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-ds-art")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: artifactConfig(group, "price", fmt.Sprintf(`
  name    = "Price"
  content = %q`, avroPriceV1)) + `
data "apicurio_artifact" "test" {
  group_id    = apicurio_artifact.test.group_id
  artifact_id = apicurio_artifact.test.artifact_id
}

resource "apicurio_artifact" "empty" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "empty"
  artifact_type = "JSON"
}

data "apicurio_artifact" "empty" {
  group_id    = apicurio_artifact.empty.group_id
  artifact_id = apicurio_artifact.empty.artifact_id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "id", group+"/price"),
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "artifact_type", "AVRO"),
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "name", "Price"),
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "version", "1"),
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "state", "ENABLED"),
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "content", avroPriceV1),
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "content_type", "application/json"),
					resource.TestCheckResourceAttr("data.apicurio_artifact.test", "references.#", "0"),
					resource.TestCheckResourceAttr("data.apicurio_artifact.empty", "artifact_type", "JSON"),
					resource.TestCheckNoResourceAttr("data.apicurio_artifact.empty", "version"),
					resource.TestCheckNoResourceAttr("data.apicurio_artifact.empty", "content"),
				),
			},
			{
				Config: `data "apicurio_artifact" "missing" {
  group_id    = "tfacc-nope"
  artifact_id = "nope"
}`,
				ExpectError: regexp.MustCompile(`Apicurio Artifact Not Found`),
			},
		},
	})
}
