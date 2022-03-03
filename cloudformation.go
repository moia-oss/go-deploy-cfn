// Package godeploycfn allows deployment of a Cloudformation template to be a bit easier
package godeploycfn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"gopkg.in/matryer/try.v1"
)

const (
	maxRetries           = 100
	maxRetryTimeForStack = time.Minute * 10
	initialRetryPeriod   = 12 * time.Second
	backoffRate          = 1.5
	maxRetryInterval     = time.Minute
)

// Cloudformation is a utility wrapper around the original aws api to make
// common operations more intuitive.
type Cloudformation struct {
	CFClient  cloudformationiface.CloudFormationAPI
	StackName string
}

// CloudformationAPI provides an API which can be used instead of a concrete client for testing/mocking purposes.
type CloudformationAPI interface {
	CloudFormationDeploy(templateBody string, namedIAM bool) error
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
	// nolint
	ecsi := &cloudformation.ExecuteChangeSetInput{
		ChangeSetName: aws.String(changeSetName),
		StackName:     aws.String(c.StackName),
	}

	_, err := c.CFClient.ExecuteChangeSet(ecsi)
	if err != nil {
		return fmt.Errorf("error executing the ChangeSet: %w", err)
	}

	// By default, it is set to 10, which may be insufficient
	try.MaxRetries = maxRetries
	startOfRetryLoop := time.Now()

	waitFor := initialRetryPeriod

	return try.Do(func(attempt int) (bool, error) {
		var err error
		// nolint
		dsi := &cloudformation.DescribeStacksInput{
			StackName: aws.String(c.StackName),
		}
		dso, err := c.CFClient.DescribeStacks(dsi)
		if err != nil {
			waitNext, doRetry, err := waitAndRetryIfAppropriate(startOfRetryLoop, waitFor, fmt.Errorf("encountered an error when describing the stack: %w"))
			waitFor = waitNext

			return doRetry, err
		}

		if len(dso.Stacks) != 1 {
			err := fmt.Errorf("unexpected (!=1) number of stacks in result: %v", len(dso.Stacks))

			return false, err
		}

		switch *dso.Stacks[0].StackStatus {
		case cloudformation.StackStatusUpdateComplete, cloudformation.StackStatusCreateComplete, cloudformation.StackStatusUpdateCompleteCleanupInProgress:
			log.Infof("ChangeSet '%s' has been successfully executed.", changeSetName)

			return false, nil
		case cloudformation.StackStatusCreateInProgress, cloudformation.StackStatusUpdateInProgress:
			waitNext, doRetry, err := waitAndRetryIfAppropriate(startOfRetryLoop, waitFor, errors.New("stack update or creation still in progress"))
			waitFor = waitNext
			return doRetry, err
		}

		return false, fmt.Errorf("unexpected stack status for stack %s: %s", *dso.Stacks[0].StackName, *dso.Stacks[0].StackStatus)
	})
}

func waitAndRetryIfAppropriate(startOfRetryLoop time.Time, waitFor time.Duration, recoverableErr error) (time.Duration, bool, error) {
	if time.Since(startOfRetryLoop) > maxRetryTimeForStack {
		return 0, false, fmt.Errorf("retryable state occurred but maximum retry period of %s has passed, so we'll stop trying: %s",
			maxRetryTimeForStack, recoverableErr)
	}

	log.Infof("Will retry again in %s. Will stop making more attempts to deploy after %s. Reason for retrying was: %s",
		waitFor.Round(time.Second), startOfRetryLoop.Add(maxRetryTimeForStack).Format(time.RFC3339), recoverableErr)

	time.Sleep(waitFor)
	return waitForNext(waitFor), true, fmt.Errorf("retryable state occurred - retrying: %s", recoverableErr)
}

func waitForNext(waitFor time.Duration) time.Duration {
	next := time.Millisecond * time.Duration(float64(waitFor.Milliseconds())*backoffRate)

	if next < maxRetryInterval {
		return next
	}

	return maxRetryInterval
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

	// nolint
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

	// nolint
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
			log.Infof("ChangeSet '%v' is empty. Nothing to do.", *ccso.Id)

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
