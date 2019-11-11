package godeploycfn

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"

	"testing"
)

type mockCFClient struct {
	cloudformationiface.CloudFormationAPI
	callsBeforeStackFinished int
	calls                    *int
}

func newMockCFClient(calls int, callsBeforeFinished int) mockCFClient {
	return mockCFClient{
		callsBeforeStackFinished: callsBeforeFinished,
		calls:                    &calls,
	}
}

// this mocked method returns a stack with no error of the name contains `update`, and returns
// an error `stackname does not exist` to satisfy the AWS behaviour when the stack is not present
// to mimic a `create`. It also returns an invalid response when the stackname contains error.
//
// in the case that as stack is in the update state, it will return StackStatusUpdateInProgress if
// the internal calls variable is not equal to a present number. In normal cases when omitted, because
// both zero-values are 0, it does nothing and the stack is always in StackStatusUpdateComplete.
func (m mockCFClient) DescribeStacks(input *cloudformation.DescribeStacksInput) (*cloudformation.DescribeStacksOutput, error) {
	if strings.Contains(*input.StackName, "update") {
		status := cloudformation.StackStatusUpdateInProgress

		if *m.calls >= m.callsBeforeStackFinished {
			status = cloudformation.StackStatusUpdateComplete
		}

		*(m.calls)++
		fmt.Printf("status %v\n", status)

		return &cloudformation.DescribeStacksOutput{
			NextToken: nil,
			Stacks: []*cloudformation.Stack{
				{
					StackName:   input.StackName,
					StackStatus: aws.String(status),
				},
			},
		}, nil
	} else if strings.Contains(*input.StackName, "error") {
		return &cloudformation.DescribeStacksOutput{
			NextToken: nil,
			Stacks:    nil,
		}, fmt.Errorf("unexpected error")
	}

	return &cloudformation.DescribeStacksOutput{
		NextToken: nil,
		Stacks:    nil,
	}, fmt.Errorf("stackname %v does not exist", *input.StackName)
}

func (m mockCFClient) ExecuteChangeSet(*cloudformation.ExecuteChangeSetInput) (*cloudformation.ExecuteChangeSetOutput, error) {
	return &cloudformation.ExecuteChangeSetOutput{}, nil
}

func TestCloudformation_executeChangeSet(t *testing.T) {
	type fields struct {
		CFClient  cloudformationiface.CloudFormationAPI
		StackName string
	}

	type args struct {
		changeSetName string
	}

	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "test execute changeset happy path",
			fields: fields{
				CFClient:  newMockCFClient(0, 1),
				StackName: "test update stack",
			},
			args: args{
				changeSetName: "foobar doesn't matter",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Cloudformation{
				CFClient:  tt.fields.CFClient,
				StackName: tt.fields.StackName,
			}
			if err := c.executeChangeSet(tt.args.changeSetName); (err != nil) != tt.wantErr {
				t.Errorf("executeChangeSet() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCloudformation_getCreateType(t *testing.T) {
	type fields struct {
		CFClient  cloudformationiface.CloudFormationAPI
		StackName string
	}

	tests := []struct {
		name    string
		fields  fields
		want    string
		wantErr bool
	}{
		{
			name: "Test getting UPDATE changeset",
			fields: fields{
				CFClient:  newMockCFClient(0, 0),
				StackName: "stack with update",
			},
			want:    "UPDATE",
			wantErr: false,
		},
		{
			name: "Test getting CREATE changeset",
			fields: fields{
				CFClient:  newMockCFClient(0, 0),
				StackName: "stack with create",
			},
			want:    "CREATE",
			wantErr: false,
		},
		{
			name: "Test getting unexpected error",
			fields: fields{
				CFClient:  newMockCFClient(0, 0),
				StackName: "stack with error",
			},
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Cloudformation{
				CFClient:  tt.fields.CFClient,
				StackName: tt.fields.StackName,
			}
			got, err := c.getCreateType()
			if (err != nil) != tt.wantErr {
				t.Errorf("getCreateType() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getCreateType() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_changeSetIsEmpty(t *testing.T) {
	type args struct {
		o *cloudformation.DescribeChangeSetOutput
	}

	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Test changeset IS empty",
			args: args{
				o: &cloudformation.DescribeChangeSetOutput{
					Status:       aws.String("FAILED"),
					StatusReason: aws.String("foobar submitted information didn't contain changes"),
				},
			},
			want: true,
		},
		{
			name: "Test changeset is not empty (other status reason)",
			args: args{
				o: &cloudformation.DescribeChangeSetOutput{
					Status:       aws.String("FAILED"),
					StatusReason: aws.String("foobar foo"),
				},
			},
			want: false,
		},
		{
			name: "Test changeset is not empty (other status)",
			args: args{
				o: &cloudformation.DescribeChangeSetOutput{
					Status:       aws.String("BANANA"),
					StatusReason: aws.String("foobar submitted information didn't contain changes"),
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := changeSetIsEmpty(tt.args.o); got != tt.want {
				t.Errorf("changeSetIsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_trimStackName(t *testing.T) {
	type args struct {
		stackName string
		max       int
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Test that trim works",
			args: args{
				stackName: "foobar",
				max:       3,
			},
			want: "foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimStackName(tt.args.stackName, tt.args.max); got != tt.want {
				t.Errorf("trimStackName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_CreateStackName(t *testing.T) {
	type args struct {
		alarmName string
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Test Global Alarm Name Generation",
			args: args{
				alarmName: "AWS/Lambda/Global/ConcurrentExecutions",
			},
			want: "aws-lambda-global-concurrentexecutions",
		},
		{
			name: "Test Lambda Alarm Name Generation 2nd Case",
			args: args{
				alarmName: "AWS/Lambda/Global/Error",
			},
			want: "aws-lambda-global-error",
		},
		{
			name: "Test Lambda Alarm Name Generation No Periods",
			args: args{
				alarmName: "AWS/Lambda/Global/Spot.foo.bar",
			},
			want: "aws-lambda-global-spot-foo-bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CreateStackName(tt.args.alarmName); got != tt.want {
				t.Errorf("createStackName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateLogicalName(t *testing.T) {
	type args struct {
		s string
	}

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "Test normal create logical name",
			args: args{
				s: "Foo/Bar-baz_bux.foo",
			},
			want: "FooBarbazbuxfoo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CreateLogicalName(tt.args.s); got != tt.want {
				t.Errorf("CreateLogicalName() = %v, want %v", got, tt.want)
			}
		})
	}
}
