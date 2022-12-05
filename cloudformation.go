// Package godeploycfn allows deployment of a Cloudformation template to be a bit easier
package godeploycfn

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	maxRetryTimeForStack = time.Minute * 10
	initialRetryPeriod   = 12 * time.Second
	maxRetryInterval     = time.Minute
)

// Cloudformation is a utility wrapper around the original aws api to make
// common operations more intuitive.
type Cloudformation struct {
	CFClient    cloudformationiface.CloudFormationAPI
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
	return *o.Status == "FAILED" && strings.Contains(*o.StatusReason, "submitted information didn't contain changes")
}

func (c *Cloudformation) getCreateType() (string, error) {
	changeSetType := "UPDATE"
	//nolint
	dsi := &cloudformation.DescribeStacksInput{
		StackName: aws.String(c.StackName),
	}

	_, err := c.CFClient.DescribeStacks(dsi)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return "", fmt.Errorf("unexpected error while describing stack: %w", err)
	}

	if err != nil {
		changeSetType = "CREATE"
	}

	return changeSetType, nil
}

func trimStackName(stackName string, max int) string {
	var sn string

	switch {
	case len(stackName) <= max:
		sn = stackName
	case len(stackName) > max:
		sn = stackName[0:max]
	}

	return sn
}

func (c *Cloudformation) executeChangeSet(changeSetName string) error {
	//nolint
	ecsi := &cloudformation.ExecuteChangeSetInput{
		ChangeSetName: aws.String(changeSetName),
		StackName:     aws.String(c.StackName),
	}

	_, err := c.CFClient.ExecuteChangeSet(ecsi)
	if err != nil {
		return fmt.Errorf("error executing the ChangeSet: %w", err)
	}

	endRetryTimestamp := time.Now().Add(maxRetryTimeForStack)

	back := &backoff.ExponentialBackOff{
		InitialInterval:     initialRetryPeriod,
		RandomizationFactor: backoff.DefaultRandomizationFactor,
		Multiplier:          backoff.DefaultMultiplier,
		MaxInterval:         maxRetryInterval,
		MaxElapsedTime:      maxRetryTimeForStack,
		Stop:                backoff.Stop,
		Clock:               backoff.SystemClock,
	}

	var errToReturn error

	err = backoff.Retry(func() error {
		var dso *cloudformation.DescribeStacksOutput

		dso, err = c.CFClient.DescribeStacks(&cloudformation.DescribeStacksInput{
			StackName: aws.String(c.StackName),
		})
		if err != nil {
			return fmt.Errorf("encountered an error when describing the stack: %w", err)
		}

		if len(dso.Stacks) != 1 {
			errToReturn = fmt.Errorf("unexpected (!=1) number of stacks in result: %v", len(dso.Stacks))

			return nil
		}

		switch *dso.Stacks[0].StackStatus {
		case cloudformation.StackStatusUpdateComplete, cloudformation.StackStatusCreateComplete, cloudformation.StackStatusUpdateCompleteCleanupInProgress:
			c.logger().Infof("ChangeSet '%s' has been successfully executed.", changeSetName)

			return nil
		case cloudformation.StackStatusCreateInProgress, cloudformation.StackStatusUpdateInProgress:
			c.logger().Infof("Encountered an error and will retry. Will stop making more attempts to deploy after %s. Reason for retrying was: %s",
				endRetryTimestamp.Format(time.RFC3339), err)

			return err
		}

		errToReturn = fmt.Errorf("unexpected stack status for stack %s: %s", *dso.Stacks[0].StackName, *dso.Stacks[0].StackStatus)

		return nil
	}, back)
	if err != nil {
		return fmt.Errorf("retryable state occurred but maximum retry period of %s has passed, so we'll stop trying: %w",
			maxRetryTimeForStack, err)
	}

	return errToReturn
}

// CloudFormationDeploy deploys the given Cloudformation Template to the given Cloudformation Stack.
func (c *Cloudformation) CloudFormationDeploy(templateBody string, namedIAM bool) error {
	changeSetType, err := c.getCreateType()
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
		ChangeSetType: aws.String(changeSetType),
		StackName:     aws.String(sn),
		TemplateBody:  aws.String(templateBody),
	}

	if namedIAM {
		ccsi.Capabilities = []*string{aws.String(cloudformation.CapabilityCapabilityNamedIam)}
	}

	ccso, err := c.CFClient.CreateChangeSet(ccsi)
	if err != nil {
		return fmt.Errorf("the ChangeSetType was %s error in creating ChangeSet: %w", changeSetType, err)
	}

	//nolint
	dcsi := &cloudformation.DescribeChangeSetInput{
		ChangeSetName: ccso.Id,
		StackName:     aws.String(sn),
	}

	maxAttempts := 12
	delay := time.Duration(5) * time.Second

	err = c.CFClient.WaitUntilChangeSetCreateCompleteWithContext(context.Background(),
		dcsi,
		request.WithWaiterDelay(request.ConstantWaiterDelay(delay)),
		request.WithWaiterMaxAttempts(maxAttempts))

	if err != nil {
		dcso, err2 := c.CFClient.DescribeChangeSet(dcsi)

		if err2 != nil {
			return fmt.Errorf("error describing the ChangeSet: %w", err2)
		}

		if changeSetIsEmpty(dcso) {
			c.logger().Infof("ChangeSet '%v' is empty. Nothing to do.", *ccso.Id)

			return nil
		}

		return fmt.Errorf("changeset is not empty but waiting for changeset completion still timed out. Error was: %w", err)
	}

	return c.executeChangeSet(csn)
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
