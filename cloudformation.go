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

// Cloudformation is a utility wrapper around the original aws api to make
// common operations more intuitive
type Cloudformation struct {
	CFClient  cloudformationiface.CloudFormationAPI
	StackName string
}

// CloudformationAPI provides an API which can be used instead of a concrete client for testing/mocking purposes
type CloudformationAPI interface {
	CloudFormationDeploy(templateBody string) error
}

func changeSetIsEmpty(o *cloudformation.DescribeChangeSetOutput) bool {
	// Seems absurd but looks like this is the best way to find out if the ChangeSet is empty.
	return *o.Status == "FAILED" && strings.Contains(*o.StatusReason, "submitted information didn't contain changes")
}

func (c *Cloudformation) getCreateType() (string, error) {
	changeSetType := "UPDATE"
	dsi := &cloudformation.DescribeStacksInput{
		StackName: aws.String(c.StackName),
	}

	_, err := c.CFClient.DescribeStacks(dsi)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return "", fmt.Errorf("unexpected error while describing stack: %s", err)
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
	ecsi := &cloudformation.ExecuteChangeSetInput{
		ChangeSetName: aws.String(changeSetName),
		StackName:     aws.String(c.StackName),
	}

	_, err := c.CFClient.ExecuteChangeSet(ecsi)
	if err != nil {
		return fmt.Errorf("error executing the ChangeSet: %s", err)
	}

	return try.Do(func(attempt int) (bool, error) {
		var err error
		dsi := &cloudformation.DescribeStacksInput{
			StackName: aws.String(c.StackName),
		}
		dso, err := c.CFClient.DescribeStacks(dsi)
		if err != nil {
			return false, fmt.Errorf("error describing the stack: %s", err)
		}

		if len(dso.Stacks) != 1 {
			return false, fmt.Errorf("unexpected (!=1) number of stacks in result: %v", len(dso.Stacks))
		}

		switch *dso.Stacks[0].StackStatus {
		case cloudformation.StackStatusUpdateComplete, cloudformation.StackStatusCreateComplete, cloudformation.StackStatusUpdateCompleteCleanupInProgress:
			log.Infof("ChangeSet '%s' has been successfully executed.", changeSetName)
			return false, nil
		case cloudformation.StackStatusCreateInProgress, cloudformation.StackStatusUpdateInProgress:
			time.Sleep(6 * time.Second)
			return attempt < 20, errors.New("stack not yet in completed state")
		}

		return false, fmt.Errorf("unexpected stack status for stack %s: %s", *dso.Stacks[0].StackName, *dso.Stacks[0].StackStatus)
	})
}

// CloudFormationDeploy deploys the given Cloudformation Template to the given Cloudformation Stack
func (c *Cloudformation) CloudFormationDeploy(templateBody string, namedIAM bool) error {
	changeSetType, err := c.getCreateType()
	if err != nil {
		return err
	}

	id, err := uuid.NewUUID()
	if err != nil {
		return fmt.Errorf("error while generating UUID %s", err)
	}

	// max stack name is 128, then we add a UUID (36 byte/char string) so the max the stackName can be is 92
	// we also add a `-' here, so adjust for that accordingly
	csn := fmt.Sprintf("%s-%s", trimStackName(c.StackName, 91), id)

	// normally, the max we can have is 128
	sn := trimStackName(c.StackName, 128)

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
		return fmt.Errorf("the ChangeSetType was %s error in creating ChangeSet: %s", changeSetType, err)
	}

	dcsi := &cloudformation.DescribeChangeSetInput{
		ChangeSetName: ccso.Id,
		StackName:     aws.String(sn),
	}

	err = c.CFClient.WaitUntilChangeSetCreateCompleteWithContext(context.Background(),
		dcsi,
		request.WithWaiterDelay(request.ConstantWaiterDelay(5*time.Second)),
		request.WithWaiterMaxAttempts(12))

	if err != nil {
		dcso, err2 := c.CFClient.DescribeChangeSet(dcsi)

		if err2 != nil {
			return fmt.Errorf("error describing the ChangeSet: %s", err2)
		}

		if changeSetIsEmpty(dcso) {
			log.Infof("ChangeSet '%v' is empty. Nothing to do.", *ccso.Id)
			return nil
		}

		return fmt.Errorf("changeset is not empty but waiting for changeset completion still timed out. "+
			"Error was: %s", err)
	}

	return c.executeChangeSet(csn)
}

// CreateStackName creates a valid stack name from the given alarm name
func CreateStackName(s string) string {
	s = strings.ToLower(s)
	for _, char := range [...]string{"/", "."} {
		s = strings.ReplaceAll(s, char, "-")
	}

	return s
}

// CreateLogicalName creates a logical name used in the CloudFormation template
func CreateLogicalName(s string) string {
	for _, char := range [...]string{"-", "/", "_", "."} {
		s = strings.ReplaceAll(s, char, "")
	}

	return s
}
