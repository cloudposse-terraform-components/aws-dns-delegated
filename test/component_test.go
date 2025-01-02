package test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/service/acm"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/cloudposse/test-helpers/pkg/atmos"
	helper "github.com/cloudposse/test-helpers/pkg/atmos/aws-component-helper"
	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type zone struct {
	Arn               string            `json:"arn"`
	Comment           string            `json:"comment"`
	DelegationSetId   string            `json:"delegation_set_id"`
	ForceDestroy      bool              `json:"force_destroy"`
	Id                string            `json:"id"`
	Name              string            `json:"name"`
	NameServers       []string          `json:"name_servers"`
	PrimaryNameServer string            `json:"primary_name_server"`
	Tags              map[string]string `json:"tags"`
	TagsAll           map[string]string `json:"tags_all"`
	Vpc               []struct {
		ID     string `json:"vpc_id"`
		Region string `json:"vpc_region"`
	} `json:"vpc"`
	ZoneID string `json:"zone_id"`
}

func TestComponent(t *testing.T) {
	awsRegion := "us-east-2"

	fixture := helper.NewFixture(t, "../", awsRegion, "test/fixtures")

	defer fixture.TearDown()
	fixture.SetUp(&atmos.Options{})

	fixture.Suite("default", func(t *testing.T, suite *helper.Suite) {
		suite.Setup(t, func(t *testing.T, atm *helper.Atmos) {
			randomID := suite.GetRandomIdentifier()
			domainName := fmt.Sprintf("example-%s.net", randomID)
			inputs := map[string]interface{}{
				"domain_names": []string{domainName},
			}
			atm.GetAndDeploy("dns-primary", "default-test", inputs)
		})

		suite.TearDown(t, func(t *testing.T, atm *helper.Atmos) {
			atm.GetAndDestroy("dns-primary", "default-test", map[string]interface{}{})
		})

		suite.Test(t, "basic", func(t *testing.T, atm *helper.Atmos) {
			dnsPrimaryComponent := helper.NewAtmosComponent("dns-primary", "default-test", map[string]interface{}{})

			primaryZones := map[string]zone{}
			atm.OutputStruct(dnsPrimaryComponent, "zones", &primaryZones)

			primaryDomains := make([]string, 0, len(primaryZones))
			for k := range primaryZones {
				primaryDomains = append(primaryDomains, k)
			}

			primaryDomainName := primaryDomains[0]
			primaryZone := primaryZones[primaryDomainName]

			delegatedDomainName := suite.GetRandomIdentifier()

			inputs := map[string]interface{}{
				"zone_config": []map[string]interface{}{
					{
						"subdomain": delegatedDomainName,
						"zone_name": primaryDomainName,
					},
				},
			}

			dnsDelegatedComponent := helper.NewAtmosComponent("dns-delegated/basic", "default-test", inputs)

			defer atm.Destroy(dnsDelegatedComponent)
			atm.Deploy(dnsDelegatedComponent)

			delegatedRecordZoneName := fmt.Sprintf("%s.%s", delegatedDomainName, primaryDomainName)
			delegatedRecordZoneNameDot := fmt.Sprintf("%s.", delegatedRecordZoneName)

			defaultDomainName := atm.Output(dnsDelegatedComponent, "default_domain_name")
			assert.Equal(t, delegatedRecordZoneName, defaultDomainName)

			delegatedZones := map[string]zone{}
			atm.OutputStruct(dnsDelegatedComponent, "zones", &delegatedZones)
			delegatedZone := delegatedZones[delegatedDomainName]

			defaultDNSZoneId := atm.Output(dnsDelegatedComponent, "default_dns_zone_id")
			assert.Equal(t, delegatedZone.ZoneID, defaultDNSZoneId)

			route53HostedZoneProtections := map[string]interface{}{}
			atm.OutputStruct(dnsDelegatedComponent, "route53_hosted_zone_protections", &route53HostedZoneProtections)
			assert.Equal(t, 0, len(route53HostedZoneProtections))

			delegatedNSRecord := aws.GetRoute53Record(t, delegatedZone.ZoneID, delegatedZone.Name, "NS", awsRegion)
			assert.Equal(t, delegatedRecordZoneNameDot, *delegatedNSRecord.Name)
			assert.EqualValues(t, 172800, *delegatedNSRecord.TTL)

			delegatedNSRecordInPrimaryZone := aws.GetRoute53Record(t, primaryZone.ZoneID, delegatedZone.Name, "NS", awsRegion)
			assert.Equal(t, delegatedRecordZoneNameDot, *delegatedNSRecordInPrimaryZone.Name)
			assert.EqualValues(t, 30, *delegatedNSRecordInPrimaryZone.TTL)

			assert.Equal(t, len(delegatedNSRecord.ResourceRecords), len(delegatedNSRecordInPrimaryZone.ResourceRecords))

			// Sort the records so we can compare them
			slices.SortFunc(delegatedNSRecord.ResourceRecords, func(a, b *route53.ResourceRecord) int {
				return strings.Compare(strings.ToLower(*a.Value), strings.ToLower(*b.Value))
			})

			slices.SortFunc(delegatedNSRecordInPrimaryZone.ResourceRecords, func(a, b *route53.ResourceRecord) int {
				return strings.Compare(strings.ToLower(*a.Value), strings.ToLower(*b.Value))
			})

			// Compare the records
			for i := range delegatedNSRecord.ResourceRecords {
				expected := *delegatedNSRecord.ResourceRecords[i].Value
				exists := fmt.Sprintf("%s.", *delegatedNSRecordInPrimaryZone.ResourceRecords[i].Value)
				assert.Equal(t, expected, exists)
			}

			type certificate struct {
				Arn                     string `json:"arn"`
				DomainValidationOptions [][]struct {
					DomainName          string `json:"domain_name"`
					ResourceRecordName  string `json:"resource_record_name"`
					ResourceRecordType  string `json:"resource_record_type"`
					ResourceRecordValue string `json:"resource_record_value"`
				} `json:"domain_validation_options"`
				Id                       string `json:"id"`
				ValidationCertificateArn string `json:"validation_certificate_arn"`
				ValidationId             string `json:"validation_id"`
			}
			// []interface{}{map[string]interface{}{"domain_name": "*.ygkxva.example-czc3n5.net", "resource_record_name": "_6b23f04661904162a9f39c79f0ba28e6.ygkxva.example-czc3n5.net.", "resource_record_type": "CNAME", "resource_record_value": "_b6e2b54cfe1568421b5f23b1b121201e.zfyfvmchrl.acm-validations.aws."}, map[string]interface{}{"domain_name": "ygkxva.example-czc3n5.net", "resource_record_name": "_6b23f04661904162a9f39c79f0ba28e6.ygkxva.example-czc3n5.net.", "resource_record_type": "CNAME", "resource_record_value": "_b6e2b54cfe1568421b5f23b1b121201e.zfyfvmchrl.acm-validations.aws."}}

			type ssmParameter struct {
				AllowedPattern string                 `json:"allowed_pattern"`
				DataType       string                 `json:"data_type"`
				Description    string                 `json:"description"`
				Id             string                 `json:"id"`
				InsecureValue  interface{}            `json:"insecure_value"`
				KeyId          string                 `json:"key_id"`
				Name           string                 `json:"name"`
				Overwrite      bool                   `json:"overwrite"`
				Tags           map[string]interface{} `json:"tags"`
				TagsAll        map[string]interface{} `json:"tags_all"`
				Tier           string                 `json:"tier"`
				Type           string                 `json:"type"`
				Value          string                 `json:"value"`
				Version        int                    `json:"version"`
			}
			acmSsmParameter := map[string]ssmParameter{}
			atm.OutputStruct(dnsDelegatedComponent, "acm_ssm_parameter", &acmSsmParameter)
			ssmParametersForDomain := acmSsmParameter[delegatedDomainName]
			ssmPath := fmt.Sprintf("/acm/%s", delegatedRecordZoneName)
			assert.Equal(t, ssmPath, ssmParametersForDomain.Id)
			assert.Equal(t, ssmPath, ssmParametersForDomain.Name)

			certificates := map[string]certificate{}
			atm.OutputStruct(dnsDelegatedComponent, "certificate", &certificates)

			assert.Equal(t, certificates[delegatedDomainName].Arn, ssmParametersForDomain.Value)
			assert.Equal(t, certificates[delegatedDomainName].Arn, ssmParametersForDomain.Value)

			client := aws.NewAcmClient(t, awsRegion)
			awsCertificate, err := client.DescribeCertificate(&acm.DescribeCertificateInput{
				CertificateArn: &ssmParametersForDomain.Value,
			})
			require.NoError(t, err)

			// We can not test issue status because DNS validation not working with mock primary domain
			// assert.Equal(t, "ISSUED", *awsCertificate.Certificate.Status)
			assert.Equal(t, "AMAZON_ISSUED", *awsCertificate.Certificate.Type)

		})
	})
}
