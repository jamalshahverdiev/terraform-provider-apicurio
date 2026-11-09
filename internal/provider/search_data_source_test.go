package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func searchFixtures(group string) string {
	return fmt.Sprintf(`
resource "apicurio_group" "test" {
  group_id      = %q
  force_destroy = true
}

resource "apicurio_artifact" "price" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "orders-price"
  artifact_type = "AVRO"
  name          = "Order price"
  description   = "The price of an order"
  labels        = { env = "prod", team = "payments" }
  content       = %q
}

resource "apicurio_artifact" "event" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "orders-event"
  artifact_type = "JSON"
  name          = "Order event"
  labels        = { env = "dev", team = "payments" }
  content       = "{}"
}

resource "apicurio_artifact" "empty" {
  group_id      = apicurio_group.test.group_id
  artifact_id   = "empty-one"
  artifact_type = "AVRO"
  labels        = { env = "prod" }
}
`, group, avroPriceV1)
}

func TestAccArtifactsDataSource(t *testing.T) {
	setupRegistry(t)
	group := randName("tfacc-search")
	cfg := searchFixtures(group) + `
locals {
  ready = [apicurio_artifact.price.id, apicurio_artifact.event.id, apicurio_artifact.empty.id]
}

data "apicurio_artifacts" "all" {
  group_id   = apicurio_group.test.group_id
  order_by   = "artifact_id"
  order      = "asc"
  depends_on = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "prod" {
  group_id   = apicurio_group.test.group_id
  labels     = { env = "prod" }
  order_by   = "artifact_id"
  depends_on = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "payments_prod" {
  group_id   = apicurio_group.test.group_id
  labels     = { env = "prod", team = "payments" }
  depends_on = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "any_team" {
  group_id   = apicurio_group.test.group_id
  labels     = { team = "" }
  order_by   = "artifact_id"
  depends_on = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "json" {
  group_id      = apicurio_group.test.group_id
  artifact_type = "JSON"
  depends_on    = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "by_name" {
  group_id   = apicurio_group.test.group_id
  name       = "Order price"
  depends_on = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "by_description" {
  group_id    = apicurio_group.test.group_id
  description = "price"
  depends_on  = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "none" {
  group_id   = apicurio_group.test.group_id
  labels     = { env = "nope" }
  depends_on = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}

data "apicurio_artifacts" "desc" {
  group_id   = apicurio_group.test.group_id
  order_by   = "artifact_id"
  order      = "desc"
  depends_on = [apicurio_artifact.price, apicurio_artifact.event, apicurio_artifact.empty]
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `data "apicurio_artifacts" "bad" { order_by = "size" }`,
				ExpectError: regexp.MustCompile(`order_by value must be one of`),
			},
			{
				Config:      `data "apicurio_artifacts" "bad" { order = "up" }`,
				ExpectError: regexp.MustCompile(`order value must be one of`),
			},
			{
				// the registry splits label filters on a colon (differently per version)
				Config:      `data "apicurio_artifacts" "bad" { labels = { url = "http://x:8080" } }`,
				ExpectError: regexp.MustCompile(`Invalid Label Filter[\s\S]*cannot contain ':'`),
			},
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.#", "3"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.0.artifact_id", "empty-one"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.1.artifact_id", "orders-event"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.2.artifact_id", "orders-price"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.2.name", "Order price"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.2.description", "The price of an order"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.2.artifact_type", "AVRO"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.2.labels.team", "payments"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.2.group_id", group),
					resource.TestCheckResourceAttrSet("data.apicurio_artifacts.all", "artifacts.2.created_on"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.0.name", ""),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.all", "artifacts.0.labels.%", "1"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.prod", "artifacts.#", "2"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.prod", "artifacts.0.artifact_id", "empty-one"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.prod", "artifacts.1.artifact_id", "orders-price"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.payments_prod", "artifacts.#", "1"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.payments_prod", "artifacts.0.artifact_id", "orders-price"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.any_team", "artifacts.#", "2"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.json", "artifacts.#", "1"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.json", "artifacts.0.artifact_id", "orders-event"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.by_name", "artifacts.#", "1"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.by_description", "artifacts.#", "1"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.none", "artifacts.#", "0"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.desc", "artifacts.0.artifact_id", "orders-price"),
					resource.TestCheckResourceAttr("data.apicurio_artifacts.desc", "artifacts.2.artifact_id", "empty-one"),
					resource.TestMatchResourceAttr("data.apicurio_artifacts.prod", "id", regexp.MustCompile(`/search/artifacts\?groupId=`+group+`&labels=env%3Aprod&orderby=artifactId$`)),
				),
			},
		},
	})
}

func TestAccArtifactTypesDataSource(t *testing.T) {
	setupRegistry(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "apicurio_artifact_types" "all" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.apicurio_artifact_types.all", "id"),
					resource.TestCheckTypeSetElemAttr("data.apicurio_artifact_types.all", "types.*", "AVRO"),
					resource.TestCheckTypeSetElemAttr("data.apicurio_artifact_types.all", "types.*", "PROTOBUF"),
					resource.TestCheckTypeSetElemAttr("data.apicurio_artifact_types.all", "types.*", "JSON"),
					resource.TestCheckTypeSetElemAttr("data.apicurio_artifact_types.all", "types.*", "XSD"),
				),
			},
		},
	})
}
