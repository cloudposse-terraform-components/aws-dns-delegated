package test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	route53Types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	acmTypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/cloudposse/test-helpers/pkg/atmos"
	helper "github.com/cloudposse/test-helpers/pkg/atmos/component-helper"
	awshelper "github.com/cloudposse/test-helpers/pkg/aws"	
	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/random"	
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/aws/aws-sdk-go-v2/service/acm"
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

type ComponentSuite struct {
	helper.TestSuite
}

func (s *ComponentSuite) TestBasic() {
	const component = "dns-delegated/basic"
	const stack = "default-test"
	const awsRegion = "us-east-2"

	const primaryDomainName = "components.cptest.test-automation.app"

	primaryZone, err := awshelper.GetDNSZoneByNameE(s.T(), context.Background(), primaryDomainName, awsRegion)
	require.NoError(s.T(), err)

	delegatedDomainName := strings.ToLower(random.UniqueId())

	inputs := map[string]interface{}{
		"zone_config": []map[string]interface{}{
			{
				"subdomain": delegatedDomainName,
				"zone_name": primaryDomainName,
			},
		},
	}

	defer s.DestroyAtmosComponent(s.T(), component, stack, &inputs)
	options, _ := s.DeployAtmosComponent(s.T(), component, stack, &inputs)
	assert.NotNil(s.T(), options)

	delegatedRecordZoneName := fmt.Sprintf("%s.%s", delegatedDomainName, primaryDomainName)
	delegatedRecordZoneNameDot := fmt.Sprintf("%s.", delegatedRecordZoneName)

	defaultDomainName := atmos.Output(s.T(), options, "default_domain_name")
	assert.Equal(s.T(), delegatedRecordZoneName, defaultDomainName)

	delegatedZones := map[string]zone{}
	atmos.OutputStruct(s.T(), options, "zones", &delegatedZones)
	delegatedZone := delegatedZones[delegatedDomainName]

	defaultDNSZoneId := atmos.Output(s.T(), options, "default_dns_zone_id")
	assert.Equal(s.T(), delegatedZone.ZoneID, defaultDNSZoneId)

	route53HostedZoneProtections := map[string]interface{}{}
	atmos.OutputStruct(s.T(), options, "route53_hosted_zone_protections", &route53HostedZoneProtections)
	assert.Empty(s.T(), route53HostedZoneProtections)

	delegatedNSRecord := aws.GetRoute53Record(s.T(), delegatedZone.ZoneID, delegatedZone.Name, "NS", awsRegion)
	assert.Equal(s.T(), delegatedRecordZoneNameDot, *delegatedNSRecord.Name)
	assert.EqualValues(s.T(), 172800, *delegatedNSRecord.TTL)

	delegatedNSRecordInPrimaryZone := aws.GetRoute53Record(s.T(), *primaryZone.Id, delegatedZone.Name, "NS", awsRegion)
	assert.Equal(s.T(), delegatedRecordZoneNameDot, *delegatedNSRecordInPrimaryZone.Name)
	assert.EqualValues(s.T(), 30, *delegatedNSRecordInPrimaryZone.TTL)

	assert.Equal(s.T(), len(delegatedNSRecord.ResourceRecords), len(delegatedNSRecordInPrimaryZone.ResourceRecords))

	// Sort the records so we can compare them
	slices.SortFunc(delegatedNSRecord.ResourceRecords, func(a, b route53Types.ResourceRecord) int {
		return strings.Compare(strings.ToLower(*a.Value), strings.ToLower(*b.Value))
	})

	slices.SortFunc(delegatedNSRecordInPrimaryZone.ResourceRecords, func(a, b route53Types.ResourceRecord) int {
		return strings.Compare(strings.ToLower(*a.Value), strings.ToLower(*b.Value))
	})

	// Compare the records
	for i := range delegatedNSRecord.ResourceRecords {
		expected := *delegatedNSRecord.ResourceRecords[i].Value
		exists := fmt.Sprintf("%s.", *delegatedNSRecordInPrimaryZone.ResourceRecords[i].Value)
		assert.Equal(s.T(), expected, exists)
	}

	acmSsmParameter := map[string]ssmParameter{}
	atmos.OutputStruct(s.T(), options, "acm_ssm_parameter", &acmSsmParameter)
	ssmParametersForDomain := acmSsmParameter[delegatedDomainName]
	ssmPath := fmt.Sprintf("/acm/%s", delegatedRecordZoneName)
	assert.Equal(s.T(), ssmPath, ssmParametersForDomain.Id)
	assert.Equal(s.T(), ssmPath, ssmParametersForDomain.Name)

	certificates := map[string]certificate{}
	atmos.OutputStruct(s.T(), options, "certificate", &certificates)

	assert.Equal(s.T(), certificates[delegatedDomainName].Arn, ssmParametersForDomain.Value)
				
	client := aws.NewAcmClient(s.T(), awsRegion)
	awsCertificate, err := client.DescribeCertificate(context.Background(), &acm.DescribeCertificateInput{
		CertificateArn: &ssmParametersForDomain.Value,
	})
	require.NoError(s.T(), err)

	// We can not test issue status because DNS validation not working with mock primary domain
	assert.Equal(s.T(), string(acmTypes.CertificateStatusIssued), string(awsCertificate.Certificate.Status))
	assert.Equal(s.T(), string(acmTypes.CertificateTypeAmazonIssued), string(awsCertificate.Certificate.Type))

	s.DriftTest(component, stack, &inputs)
}

func (s *ComponentSuite) TestEnabledFlag() {
	const component = "dns-delegated/disabled"
	const stack = "default-test"
	const awsRegion = "us-east-2"
	
	const primaryDomainName = "components.cptest.test-automation.app"
	
	delegatedDomainName := strings.ToLower(random.UniqueId())

	inputs := map[string]interface{}{
		"zone_config": []map[string]interface{}{
			{
				"subdomain": delegatedDomainName,
				"zone_name": primaryDomainName,
			},
		},
	}

	s.VerifyEnabledFlag(component, stack, &inputs)
}


func TestRunSuite(t *testing.T) {
	suite := new(ComponentSuite)
	helper.Run(t, suite)
}
