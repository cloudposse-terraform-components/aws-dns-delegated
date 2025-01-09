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

func TestComponent(t *testing.T) {
	awsRegion := "us-east-2"

	fixture := helper.NewFixture(t, "../", awsRegion, "test/fixtures")

	defer fixture.TearDown()
	fixture.SetUp(&atmos.Options{})

	fixture.Suite("default", func(t *testing.T, suite *helper.Suite) {
		suite.Test(t, "basic", func(t *testing.T, atm *helper.Atmos) {
			primaryDomainName := "components.cptest.test-automation.app"
			primaryZone, err := GetDNSZoneByNameE(t, primaryDomainName, awsRegion)

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

			delegatedNSRecordInPrimaryZone := aws.GetRoute53Record(t, *primaryZone.Id, delegatedZone.Name, "NS", awsRegion)
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
			assert.Equal(t, "ISSUED", *awsCertificate.Certificate.Status)
			assert.Equal(t, "AMAZON_ISSUED", *awsCertificate.Certificate.Type)
		})
	})
}

func GetDNSZoneByNameE(t *testing.T, hostName string, awsRegion string) (*route53.HostedZone, error) {
	client, err := aws.NewRoute53ClientE(t, awsRegion)
	if err != nil {
		return nil, err
	}

	response, err := client.ListHostedZonesByName(&route53.ListHostedZonesByNameInput{DNSName: &hostName})
	if err != nil {
		return nil, err
	}
	if len(response.HostedZones) == 0 {
		return nil, fmt.Errorf("no hosted zones found for %s", hostName)
	}
	return response.HostedZones[0], nil
}
