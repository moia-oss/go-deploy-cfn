// Package godeploycfn allows deployment of a Cloudformation template to be a bit easier
package godeploycfn

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/google/uuid"
	"github.com/sethvargo/go-retry"
	"github.com/sirupsen/logrus"
)

const (
	maxRetryTimeForStack = time.Minute * 10
	initialRetryPeriod   = 12 * time.Second
	maxRetryInterval     = time.Minute
)

// CloudFormationClient defines the interface for CloudFormation operations.
type CloudFormationClient interface {
	DescribeStacks(ctx context.Context, params *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
	CreateChangeSet(ctx context.Context, params *cloudformation.CreateChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error)
	DescribeChangeSet(ctx context.Context, params *cloudformation.DescribeChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error)
	DeleteChangeSet(ctx context.Context, params *cloudformation.DeleteChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error)
	ExecuteChangeSet(ctx context.Context, params *cloudformation.ExecuteChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error)
}

// Cloudformation is a utility wrapper around the original aws api to make
// common operations more intuitive.
type Cloudformation struct {
	CFClient    CloudFormationClient
	StackName   string
	LogrusEntry *logrus.Entry
}

// CloudformationAPI provides an API which can be used instead of a concrete client for testing/mocking purposes.
type CloudformationAPI interface {
	CloudFormationDeploy(templateBody string, namedIAM bool) error
}

func (c *Cloudformation) logger() *logrus.Entry {
	stackFields := logrus.Fields{
		"stack_name": c.StackName,
	}

	if c.LogrusEntry == nil {
		return logrus.WithFields(stackFields)
	}

	return c.LogrusEntry.WithFields(stackFields)
}

func changeSetIsEmpty(o *cloudformation.DescribeChangeSetOutput) bool {
	// Seems absurd but looks like this is the best way to find out if the ChangeSet is empty.
	return o.Status == types.ChangeSetStatusFailed && o.StatusReason != nil && strings.Contains(*o.StatusReason, "submitted information didn't contain changes")
}

func (c *Cloudformation) getCreateType(ctx context.Context) (types.ChangeSetType, error) {
	changeSetType := types.ChangeSetTypeUpdate
	//nolint
	dsi := &cloudformation.DescribeStacksInput{
		StackName: aws.String(c.StackName),
	}

	_, err := c.CFClient.DescribeStacks(ctx, dsi)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return "", fmt.Errorf("unexpected error while describing stack: %w", err)
	}

	if err != nil {
		changeSetType = types.ChangeSetTypeCreate
	}

	return changeSetType, nil
}

func trimStackName(stackName string, maxLen int) string {
	var sn string

	switch {
	case len(stackName) <= maxLen:
		sn = stackName
	case len(stackName) > maxLen:
		sn = stackName[0:maxLen]
	}

	return sn
}

func (c *Cloudformation) executeChangeSet(ctx context.Context, changeSetName string) error {
	//nolint
	ecsi := &cloudformation.ExecuteChangeSetInput{
		ChangeSetName: aws.String(changeSetName),
		StackName:     aws.String(c.StackName),
	}

	_, err := c.CFClient.ExecuteChangeSet(ctx, ecsi)
	if err != nil {
		return fmt.Errorf("error executing the ChangeSet: %w", err)
	}

	endRetryTimestamp := time.Now().Add(maxRetryTimeForStack)

	backoff := retry.NewFibonacci(initialRetryPeriod)
	backoff = retry.WithCappedDuration(maxRetryInterval, backoff)

	// Create a context with timeout for the retry loop
	retryCtx, cancel := context.WithTimeout(ctx, maxRetryTimeForStack)
	defer cancel()

	var errToReturn error

	err = retry.Do(retryCtx, backoff, func(ctx context.Context) error {
		var dso *cloudformation.DescribeStacksOutput

		dso, err = c.CFClient.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
			NextToken: nil,
			StackName: aws.String(c.StackName),
		})
		if err != nil {
			return retry.RetryableError(fmt.Errorf("encountered an error when describing the stack: %w", err))
		}

		if len(dso.Stacks) != 1 {
			errToReturn = fmt.Errorf("unexpected (!=1) number of stacks in result: %v", len(dso.Stacks))

			return nil
		}

		stackStatus := dso.Stacks[0].StackStatus
		switch stackStatus {
		case types.StackStatusUpdateComplete, types.StackStatusCreateComplete, types.StackStatusUpdateCompleteCleanupInProgress:
			c.logger().Infof("ChangeSet '%s' has been successfully executed.", changeSetName)

			return nil
		case types.StackStatusCreateInProgress, types.StackStatusUpdateInProgress:
			c.logger().Infof("Stack update still in progress. Will check again. Will stop making more attempts to deploy after %s.",
				endRetryTimestamp.Format(time.RFC3339))

			return retry.RetryableError(fmt.Errorf("stack creation not complete yet, status: %s", stackStatus))
		}

		errToReturn = fmt.Errorf("unexpected stack status for stack %s: %s", *dso.Stacks[0].StackName, stackStatus)

		return nil
	})
	if err != nil {
		return fmt.Errorf("retryable state occurred but maximum retry period of %s has passed, so we'll stop trying: %w",
			maxRetryTimeForStack, err)
	}

	return errToReturn
}

// CloudFormationDeploy deploys the given Cloudformation Template to the given Cloudformation Stack.
func (c *Cloudformation) CloudFormationDeploy(templateBody string, namedIAM bool) error {
	ctx := context.Background()

	changeSetType, err := c.getCreateType(ctx)
	if err != nil {
		return err
	}

	id, err := uuid.NewUUID()
	if err != nil {
		return fmt.Errorf("error while generating UUID %w", err)
	}

	// max stack name is 128, then we add a UUID (36 byte/char string) so the max the stackName can be is 92
	// we also add a `-' here, so adjust for that accordingly
	csn := fmt.Sprintf("%s-%s", trimStackName(c.StackName, 91), id)

	// normally, the max we can have is 128
	sn := trimStackName(c.StackName, 128)

	//nolint
	ccsi := &cloudformation.CreateChangeSetInput{
		ChangeSetName: aws.String(csn),
		ChangeSetType: changeSetType,
		StackName:     aws.String(sn),
		TemplateBody:  aws.String(templateBody),
	}

	if namedIAM {
		ccsi.Capabilities = []types.Capability{types.CapabilityCapabilityNamedIam}
	}

	ccso, err := c.CFClient.CreateChangeSet(ctx, ccsi)
	if err != nil {
		return fmt.Errorf("the ChangeSetType was %s error in creating ChangeSet: %w", changeSetType, err)
	}

	//nolint
	dcsi := &cloudformation.DescribeChangeSetInput{
		ChangeSetName: ccso.Id,
		StackName:     aws.String(sn),
	}

	// Wait for changeset to be created with exponential backoff
	changeSetBackoff := retry.NewFibonacci(5 * time.Second)
	changeSetBackoff = retry.WithCappedDuration(30*time.Second, changeSetBackoff)

	// Create a context with timeout for the changeset wait
	changeSetCtx, cancelChangeSet := context.WithTimeout(ctx, time.Minute)
	defer cancelChangeSet()

	var dcso *cloudformation.DescribeChangeSetOutput

	err = retry.Do(changeSetCtx, changeSetBackoff, func(ctx context.Context) error {
		dcso, err = c.CFClient.DescribeChangeSet(ctx, dcsi)
		if err != nil {
			return fmt.Errorf("error describing the ChangeSet: %w", err)
		}

		if dcso.Status == types.ChangeSetStatusCreateComplete || dcso.Status == types.ChangeSetStatusFailed {
			return nil
		}

		return retry.RetryableError(fmt.Errorf("changeset not ready yet, status: %s", dcso.Status))
	})
	if err != nil {
		return fmt.Errorf("waiting for changeset creation timed out: %w", err)
	}

	if dcso.Status != types.ChangeSetStatusCreateComplete {
		if changeSetIsEmpty(dcso) {
			c.logger().Infof("ChangeSet '%v' is empty. Deleting again.", *ccso.Id)

			_, err3 := c.CFClient.DeleteChangeSet(ctx, &cloudformation.DeleteChangeSetInput{
				ChangeSetName: aws.String(csn),
				StackName:     aws.String(sn),
			})
			if err3 != nil {
				return fmt.Errorf("couldn't delete empty change set: %w", err3)
			}

			return nil
		}

		return fmt.Errorf("changeset is not empty but waiting for changeset completion still timed out. Status: %s", dcso.Status)
	}

	return c.executeChangeSet(ctx, csn)
}

// CreateStackName creates a valid stack name from the given alarm name.
func CreateStackName(s string) string {
	s = strings.ToLower(s)
	for _, char := range [...]string{"/", "."} {
		s = strings.ReplaceAll(s, char, "-")
	}

	return s
}

// CreateLogicalName creates a logical name used in the CloudFormation template.
func CreateLogicalName(s string) string {
	for _, char := range [...]string{"-", "/", "_", ".", " "} {
		s = strings.ReplaceAll(s, char, "")
	}

	return s
}
