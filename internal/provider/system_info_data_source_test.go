package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccSystemInfoDataSource(t *testing.T) {
	setupMock(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "apicurio_system_info" "this" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.apicurio_system_info.this", "id"),
					resource.TestCheckResourceAttrSet("data.apicurio_system_info.this", "name"),
					resource.TestCheckResourceAttrSet("data.apicurio_system_info.this", "built_on"),
					resource.TestCheckResourceAttrWith("data.apicurio_system_info.this", "version", func(v string) error {
						if liveServer() {
							if !regexp.MustCompile(`^3\.\d+\.\d+`).MatchString(v) {
								return fmt.Errorf("version %q is not a 3.x version", v)
							}
							return nil
						}
						if v != mockVersion {
							return fmt.Errorf("version = %q, want %q", v, mockVersion)
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccSystemInfoDataSourceUnreachable(t *testing.T) {
	if liveServer() {
		t.Skip("only meaningful against the fake server")
	}
	clearEnv(t)
	t.Setenv("APICURIO_URL", mockURL)
	activateMock(t)
	// No responder: httpmock answers every request with an error.
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `data "apicurio_system_info" "this" {}`,
				ExpectError: regexp.MustCompile(`Unable to Reach the Apicurio Registry`),
			},
		},
	})
}
