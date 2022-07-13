package account

import (
	"fmt"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	awsv1alpha1 "github.com/openshift/aws-account-operator/pkg/apis/aws/v1alpha1"
	"github.com/openshift/aws-account-operator/pkg/awsclient"
	"github.com/openshift/aws-account-operator/pkg/awsclient/mock"
	"github.com/openshift/aws-account-operator/pkg/controller/testutils"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

type testRunInstanceInputBuilder struct {
	instanceInput ec2.RunInstancesInput
}

func newTestRunInstanceInputBuilder() *testRunInstanceInputBuilder {
	commonTags := []*ec2.Tag{
		{
			Key:   aws.String("clusterAccountName"),
			Value: aws.String(""),
		},
		{
			Key:   aws.String("clusterNamespace"),
			Value: aws.String(""),
		},
		{
			Key:   aws.String("clusterClaimLink"),
			Value: aws.String(""),
		},
		{
			Key:   aws.String("clusterClaimLinkNamespace"),
			Value: aws.String(""),
		},
		{
			Key:   aws.String("Name"),
			Value: aws.String("red-hat-region-init"),
		},
	}
	input := ec2.RunInstancesInput{
		BlockDeviceMappings: []*ec2.BlockDeviceMapping{
			{
				DeviceName: aws.String("/dev/sda1"),
				Ebs: &ec2.EbsBlockDevice{
					DeleteOnTermination: aws.Bool(true),
					Encrypted:           aws.Bool(true),
					VolumeSize:          aws.Int64(10),
				},
			},
		},
		ImageId:      aws.String("fakeami"),
		InstanceType: aws.String("t2.micro"),
		MaxCount:     aws.Int64(1),
		MinCount:     aws.Int64(1),
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: &awsv1alpha1.InstanceResourceType,
				Tags:         commonTags,
			},
			{
				ResourceType: aws.String("volume"),
				Tags:         commonTags,
			},
		},
	}
	return &testRunInstanceInputBuilder{
		instanceInput: input,
	}
}

func (inputbuilder *testRunInstanceInputBuilder) WithKmsKeyId(kmsKeyId string) *testRunInstanceInputBuilder {
	inputbuilder.instanceInput.BlockDeviceMappings[0].Ebs.KmsKeyId = &kmsKeyId
	return inputbuilder
}

func TestCreateSubnet(t *testing.T) {
	tests := []struct {
		Name             string
		AwsAccount       *awsv1alpha1.Account
		ManagedTags      []awsclient.AWSTag
		CustomTags       []awsclient.AWSTag
		CidrBlock        string
		VpcID            string
		ExpectedSubnetID string
		ExpectError      bool
	}{
		{
			Name: "positive",
			AwsAccount: &awsv1alpha1.Account{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-cluster-namespace",
				},
				Spec: awsv1alpha1.AccountSpec{
					ClaimLink:          "test-claim-link",
					ClaimLinkNamespace: "test-claim-link-namespace",
				},
			},
			ManagedTags: []awsclient.AWSTag{
				{
					Key:   "openshift",
					Value: "managed",
				},
			},
			CustomTags: []awsclient.AWSTag{
				{
					Key:   "custom",
					Value: "yes",
				},
			},
			CidrBlock:        "10.0.0.0/16",
			VpcID:            "testVPC",
			ExpectedSubnetID: "subnet",
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			logger := testutils.NullLogger{}

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			expectedSubnetInputTags := []awsclient.AWSTag{}
			expectedSubnetInputTags = append(expectedSubnetInputTags, awsclient.AWSTag{
				Key:   "clusterAccountName",
				Value: test.AwsAccount.Name,
			})
			expectedSubnetInputTags = append(expectedSubnetInputTags, awsclient.AWSTag{
				Key:   "clusterNamespace",
				Value: test.AwsAccount.Namespace,
			})
			expectedSubnetInputTags = append(expectedSubnetInputTags, awsclient.AWSTag{
				Key:   "clusterClaimLink",
				Value: test.AwsAccount.Spec.ClaimLink,
			})
			expectedSubnetInputTags = append(expectedSubnetInputTags, awsclient.AWSTag{
				Key:   "clusterClaimLinkNamespace",
				Value: test.AwsAccount.Spec.ClaimLinkNamespace,
			})
			expectedSubnetInputTags = append(expectedSubnetInputTags, test.ManagedTags...)
			expectedSubnetInputTags = append(expectedSubnetInputTags, test.CustomTags...)

			mockAWSClient := mock.NewMockClient(ctrl)
			tags := awsclient.AWSAccountOperatorTags{
				Tags: expectedSubnetInputTags,
			}

			csi := &ec2.CreateSubnetInput{
				VpcId:     aws.String(test.VpcID),
				CidrBlock: aws.String(test.CidrBlock),
				TagSpecifications: []*ec2.TagSpecification{
					{
						ResourceType: aws.String("subnet"),
						Tags:         tags.GetEC2Tags(),
					},
				},
			}
			mockAWSClient.EXPECT().CreateSubnet(csi).Return(&ec2.CreateSubnetOutput{
				Subnet: &ec2.Subnet{
					SubnetId: aws.String("subnet"),
				},
			}, nil)

			actualSubnetID, err := createSubnet(logger, mockAWSClient, test.AwsAccount, test.ManagedTags, test.CustomTags, test.CidrBlock, test.VpcID)
			if test.ExpectError == (err == nil) {
				t.Errorf("createSubnet() %s: ExpectError: %t, actual error: %s\n", test.Name, test.ExpectError, err)
			}

			if actualSubnetID != test.ExpectedSubnetID {
				t.Errorf("createSubnet() %s: ExpectedSubnetID: %s, actualSubnetID: %s", test.Name, test.ExpectedSubnetID, actualSubnetID)
			}
		})
	}
}

func TestDeleteSubnet(t *testing.T) {
	tests := []struct {
		Name        string
		SubnetID    string
		ReturnError error
		ExpectError bool
	}{
		{
			Name:        "positive",
			SubnetID:    "subnet-1234",
			ReturnError: nil,
			ExpectError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			logger := testutils.NullLogger{}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockAWSClient := mock.NewMockClient(ctrl)

			// the DeleteSubnetOutput is dropped in deleteSubnet()
			mockAWSClient.EXPECT().DeleteSubnet(&ec2.DeleteSubnetInput{
				SubnetId: aws.String(test.SubnetID),
			}).Return(nil, test.ReturnError)

			err := deleteSubnet(logger, mockAWSClient, test.SubnetID)
			if test.ExpectError == (err == nil) {
				t.Errorf("DeleteSubnet() %s: ExpectError: %t, actual error: %s\n", test.Name, test.ExpectError, err)
			}

		})
	}
}

func TestCreateEC2Instance(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockAWSClient := mock.NewMockClient(ctrl)
	instanceInfo := awsv1alpha1.AmiSpec{
		Ami:          "fakeami",
		InstanceType: "t2.micro",
	}
	type args struct {
		reqLogger           logr.Logger
		account             *awsv1alpha1.Account
		client              awsclient.Client
		instanceInfo        awsv1alpha1.AmiSpec
		managedTags         []awsclient.AWSTag
		customerTags        []awsclient.AWSTag
		customerKmsKeyId    string
		instanceInput       *ec2.RunInstancesInput
		instanceOutput      *ec2.Reservation
		instanceOutputError error
	}
	tests := []struct {
		name     string
		args     args
		expected string
		wantErr  bool
	}{
		{"Start instance without customer supplied key", args{
			reqLogger:        testutils.NullLogger{},
			account:          &newTestAccountBuilder().acct,
			client:           mockAWSClient,
			instanceInfo:     instanceInfo,
			managedTags:      []awsclient.AWSTag{},
			customerTags:     []awsclient.AWSTag{},
			customerKmsKeyId: "",
			instanceInput:    &newTestRunInstanceInputBuilder().instanceInput,
			instanceOutput: &ec2.Reservation{
				Groups: []*ec2.GroupIdentifier{},
				Instances: []*ec2.Instance{
					{
						InstanceId: aws.String("1"),
					},
				},
				OwnerId:       aws.String("red-hat"),
				RequesterId:   aws.String("aao"),
				ReservationId: aws.String("1"),
			},
			instanceOutputError: nil,
		}, "1", false},
		{"Start instance with customer supplied key", args{
			reqLogger:        testutils.NullLogger{},
			account:          &newTestAccountBuilder().acct,
			client:           mockAWSClient,
			instanceInfo:     instanceInfo,
			managedTags:      []awsclient.AWSTag{},
			customerTags:     []awsclient.AWSTag{},
			customerKmsKeyId: "123456",
			instanceInput:    &newTestRunInstanceInputBuilder().WithKmsKeyId("123456").instanceInput,
			instanceOutput: &ec2.Reservation{
				Groups: []*ec2.GroupIdentifier{},
				Instances: []*ec2.Instance{
					{
						InstanceId: aws.String("1"),
					},
				},
				OwnerId:       aws.String("red-hat"),
				RequesterId:   aws.String("aao"),
				ReservationId: aws.String("1"),
			},
			instanceOutputError: nil,
		}, "1", false},
		{"Failing to start intances return error", args{
			reqLogger:        testutils.NullLogger{},
			account:          &newTestAccountBuilder().acct,
			client:           mockAWSClient,
			instanceInfo:     instanceInfo,
			managedTags:      []awsclient.AWSTag{},
			customerTags:     []awsclient.AWSTag{},
			customerKmsKeyId: "",
			instanceInput:    &newTestRunInstanceInputBuilder().instanceInput,
			instanceOutput: &ec2.Reservation{
				Groups:        []*ec2.GroupIdentifier{},
				Instances:     []*ec2.Instance{},
				OwnerId:       aws.String("red-hat"),
				RequesterId:   aws.String("aao"),
				ReservationId: aws.String("1"),
			},
			instanceOutputError: awserr.New("Test", "Test", fmt.Errorf("Test")),
		}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockAWSClient.EXPECT().RunInstances(tt.args.instanceInput).MinTimes(1).MaxTimes(1).Return(tt.args.instanceOutput, tt.args.instanceOutputError)
			got, err := CreateEC2Instance(tt.args.reqLogger, tt.args.account, tt.args.client, tt.args.instanceInfo, tt.args.managedTags, tt.args.customerTags, tt.args.customerKmsKeyId)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateEC2Instance() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("CreateEC2Instance() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestReconcileAccount_InitializeSupportedRegions(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockAWSBuilder := mock.NewMockIBuilder(ctrl)
	mockAWSClient := mock.NewMockClient(ctrl)
	mockAWSBuilder.EXPECT().GetClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(mockAWSClient, nil)
	mockAWSClient.EXPECT().DescribeInstances(gomock.Any()).Return(&ec2.DescribeInstancesOutput{}, nil).MinTimes(2).MaxTimes(3)
	mockAWSClient.EXPECT().RunInstances(gomock.Any()).Return(&ec2.Reservation{
		Groups: []*ec2.GroupIdentifier{},
		Instances: []*ec2.Instance{
			{
				InstanceId: aws.String("1"),
			},
		},
		OwnerId:       aws.String("red-hat"),
		RequesterId:   aws.String("aao"),
		ReservationId: aws.String("1"),
	}, nil)
	mockAWSClient.EXPECT().DescribeInstanceStatus(gomock.Any()).Return(&ec2.DescribeInstanceStatusOutput{
		InstanceStatuses: []*ec2.InstanceStatus{
			{
				InstanceState: &ec2.InstanceState{
					Code: aws.Int64(16),
					Name: aws.String("Running"),
				},
			},
		},
	}, nil)
	mockAWSClient.EXPECT().TerminateInstances(gomock.Any()).Return(&ec2.TerminateInstancesOutput{}, nil)
	type fields struct {
		Client           client.Client
		scheme           *runtime.Scheme
		awsClientBuilder awsclient.IBuilder
		shardName        string
	}
	type args struct {
		reqLogger  logr.Logger
		account    *awsv1alpha1.Account
		regions    []awsv1alpha1.AwsRegions
		creds      *sts.AssumeRoleOutput
		regionAMIs map[string]awsv1alpha1.AmiSpec
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{"Log failure to retrieve KMS Key from claim.",
			fields{
				Client:           fake.NewFakeClient(),
				scheme:           scheme.Scheme,
				awsClientBuilder: mockAWSBuilder,
				shardName:        "test",
			}, args{
				reqLogger: &testutils.TestLogger{},
				account:   &awsv1alpha1.Account{},
				regions: []awsv1alpha1.AwsRegions{
					{
						Name: "us-east-1",
					}},
				creds: &sts.AssumeRoleOutput{
					AssumedRoleUser: &sts.AssumedRoleUser{},
					Credentials: &sts.Credentials{
						AccessKeyId:     aws.String("123456"),
						Expiration:      &time.Time{},
						SecretAccessKey: aws.String("123456"),
						SessionToken:    aws.String("123456"),
					},
					PackedPolicySize: new(int64),
				},
				regionAMIs: map[string]awsv1alpha1.AmiSpec{},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ReconcileAccount{
				Client:           tt.fields.Client,
				scheme:           tt.fields.scheme,
				awsClientBuilder: tt.fields.awsClientBuilder,
				shardName:        tt.fields.shardName,
			}
			r.InitializeSupportedRegions(tt.args.reqLogger, tt.args.account, tt.args.regions, tt.args.creds, tt.args.regionAMIs)
			assert.Contains(t, tt.args.reqLogger.(*testutils.TestLogger).Output, "Could not retrieve account claim for account. [account ]")
		})
	}
}

func TestCreateVpc(t *testing.T) {
	tests := []struct {
		Name                   string
		Account                *awsv1alpha1.Account
		ManagedTags            []awsclient.AWSTag
		CustomTags             []awsclient.AWSTag
		ExpectedCreateVpcInput *ec2.CreateVpcInput
	}{
		{
			Name:    "positive",
			Account: newTestAccountBuilder().BYOC(true).GetTestAccount(),
			ManagedTags: []awsclient.AWSTag{
				awsclient.AWSTag{
					Key:   "Name",
					Value: "managed-openshift-cluster",
				},
			},
			CustomTags: []awsclient.AWSTag{},
			ExpectedCreateVpcInput: &ec2.CreateVpcInput{
				CidrBlock: aws.String("10.0.0.0/16"),
				TagSpecifications: []*ec2.TagSpecification{
					{
						ResourceType: aws.String("vpc"),
						Tags: []*ec2.Tag{
							{
								Key:   aws.String("clusterAccountName"),
								Value: aws.String(""),
							},
							{
								Key:   aws.String("clusterNamespace"),
								Value: aws.String(""),
							},
							{
								Key:   aws.String("clusterClaimLink"),
								Value: aws.String(""),
							},
							{
								Key:   aws.String("clusterClaimLinkNamespace"),
								Value: aws.String(""),
							},
							{
								Key:   aws.String("Name"),
								Value: aws.String("managed-openshift-cluster"),
							},
							{
								Key:   aws.String("Name"),
								Value: aws.String("red-hat-region-init"),
							},
						},
					},
				},
			},
		},
	}

	logger := &testutils.TestLogger{}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockAWSClient := mock.NewMockClient(ctrl)
			mockAWSClient.EXPECT().CreateVpc(test.ExpectedCreateVpcInput).Return(&ec2.CreateVpcOutput{
				Vpc: &ec2.Vpc{
					VpcId: aws.String("fakeVpcId"),
				},
			}, nil)

			_, err := createVpc(logger, mockAWSClient, test.Account, test.ManagedTags, test.CustomTags)
			if err != nil {
				t.Errorf("unexpected error: %s\n", err)
			}
		})
	}
}

func TestDeleteFedrampInitializationResources(t *testing.T) {
	tests := []struct {
		Name                        string
		VpcID                       string
		ReturnDescribeSubnetsOutput *ec2.DescribeSubnetsOutput
		ReturnError                 error
		ExpectError                 bool
	}{
		{
			Name:                        "positive",
			VpcID:                       "vpc-test",
			ReturnDescribeSubnetsOutput: &ec2.DescribeSubnetsOutput{},
			ReturnError:                 nil,
			ExpectError:                 false,
		},
	}

	logger := &testutils.TestLogger{}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockAWSClient := mock.NewMockClient(ctrl)
			mockAWSClient.EXPECT().DescribeSubnets(&ec2.DescribeSubnetsInput{
				DryRun: aws.Bool(true),
			}).Return(nil, test.ReturnError)
			mockAWSClient.EXPECT().DescribeSubnets(&ec2.DescribeSubnetsInput{
				Filters: []*ec2.Filter{
					{
						Name: aws.String("vpc-id"),
						Values: []*string{
							&test.VpcID,
						},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterAccountName")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterNamespace")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLink")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLinkNamespace")},
					},
				},
			}).Return(&ec2.DescribeSubnetsOutput{}, test.ReturnError)

			mockAWSClient.EXPECT().DeleteVpc(&ec2.DeleteVpcInput{
				VpcId: aws.String(test.VpcID),
			})

			err := deleteFedrampInitializationResources(logger, mockAWSClient, test.VpcID)
			if test.ExpectError == (err == nil) {
				t.Errorf("ListHostedZones() %s: ExpectError: %t, actual error: %s\n", test.Name, test.ExpectError, err)
			}

		})
	}
}

func TestCleanFedrampInitializationResources(t *testing.T) {
	tests := []struct {
		Name                        string
		AccountName                 string
		Region                      string
		VpcID                       string
		SubnetID                    string
		ReturnDescribeVpcsOutput    *ec2.DescribeVpcsOutput
		ReturnDescribeSubnetsOutput *ec2.DescribeSubnetsOutput
		ExpectCleaned               bool
		ReturnError                 error
		ExpectError                 bool
	}{
		{
			Name:        "positive",
			AccountName: "test-account",
			Region:      "us-nowhere-0",
			VpcID:       "vpc-test",
			SubnetID:    "subnet-test",
			ReturnDescribeVpcsOutput: &ec2.DescribeVpcsOutput{
				Vpcs: []*ec2.Vpc{
					{
						VpcId: aws.String("vpc-test"),
					},
				},
			},
			ReturnDescribeSubnetsOutput: &ec2.DescribeSubnetsOutput{
				Subnets: []*ec2.Subnet{
					{
						SubnetId: aws.String("subnet-test"),
					},
				},
			},
			ExpectCleaned: true,
			ReturnError:   nil,
			ExpectError:   false,
		},
	}

	logger := &testutils.TestLogger{}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockAWSClient := mock.NewMockClient(ctrl)
			mockAWSClient.EXPECT().DescribeVpcs(&ec2.DescribeVpcsInput{
				DryRun: aws.Bool(true),
			}).Return(nil, test.ReturnError)
			mockAWSClient.EXPECT().DescribeVpcs(&ec2.DescribeVpcsInput{
				Filters: []*ec2.Filter{
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterAccountName")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterNamespace")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLink")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLinkNamespace")},
					},
				},
				MaxResults: aws.Int64(5),
			}).Return(test.ReturnDescribeVpcsOutput, test.ReturnError)
			mockAWSClient.EXPECT().DescribeSubnets(&ec2.DescribeSubnetsInput{
				DryRun: aws.Bool(true),
			}).Return(nil, test.ReturnError)
			mockAWSClient.EXPECT().DescribeSubnets(&ec2.DescribeSubnetsInput{
				Filters: []*ec2.Filter{
					{
						Name:   aws.String("vpc-id"),
						Values: []*string{&test.VpcID},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterAccountName")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterNamespace")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLink")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLinkNamespace")},
					},
				},
			}).Return(test.ReturnDescribeSubnetsOutput, test.ReturnError)
			mockAWSClient.EXPECT().DeleteSubnet(&ec2.DeleteSubnetInput{SubnetId: aws.String(test.SubnetID)}).Return(&ec2.DeleteSubnetOutput{}, test.ReturnError)
			mockAWSClient.EXPECT().DeleteVpc(&ec2.DeleteVpcInput{VpcId: aws.String(test.VpcID)}).Return(&ec2.DeleteVpcOutput{}, test.ReturnError)

			actuallyCleaned, err := cleanFedrampInitializationResources(logger, mockAWSClient, test.AccountName, test.Region)
			if test.ExpectError == (err == nil) {
				t.Errorf("cleanFedrampInitializationResources() %s: ExpectError: %t, actual error: %s\n", test.Name, test.ExpectError, err)
			}

			if actuallyCleaned != test.ExpectCleaned {
				t.Errorf("cleanFedrampInitializationResources() %s: ExpectCleaned: %t, actuallyCleaned: %t\n", test.Name, test.ExpectCleaned, actuallyCleaned)
			}
		})
	}
}

func TestCleanFedrampSubnet(t *testing.T) {
	tests := []struct {
		Name                               string
		VpcId                              string
		ExpectedDryRunDescribeSubnetsInput *ec2.DescribeSubnetsInput
		ExpectedRealDescribeSubnetsInput   *ec2.DescribeSubnetsInput
		ExpectedRealDescribeSubnetsOutput  *ec2.DescribeSubnetsOutput
	}{
		{
			Name:                               "positive",
			VpcId:                              "example",
			ExpectedDryRunDescribeSubnetsInput: &ec2.DescribeSubnetsInput{DryRun: aws.Bool(true)},
			ExpectedRealDescribeSubnetsInput: &ec2.DescribeSubnetsInput{
				Filters: []*ec2.Filter{
					{
						Name:   aws.String("vpc-id"),
						Values: []*string{aws.String("example")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterAccountName")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterNamespace")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLink")},
					},
					{
						Name:   aws.String("tag-key"),
						Values: []*string{aws.String("clusterClaimLinkNamespace")},
					},
				},
			},
			ExpectedRealDescribeSubnetsOutput: &ec2.DescribeSubnetsOutput{
				Subnets: []*ec2.Subnet{},
			},
		},
	}

	logger := &testutils.TestLogger{}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockAWSClient := mock.NewMockClient(ctrl)
			mockAWSClient.EXPECT().DescribeSubnets(test.ExpectedDryRunDescribeSubnetsInput)
			mockAWSClient.EXPECT().DescribeSubnets(test.ExpectedRealDescribeSubnetsInput).Return(test.ExpectedRealDescribeSubnetsOutput, nil)

			err := cleanFedrampSubnet(logger, mockAWSClient, test.VpcId)
			if err != nil {
				t.Errorf("unexpected error: %s\n", err)
			}
		})
	}
}