# go-deploy-cfn

A helper library which can be used in a Go project to deploy a cloudformation yaml template easier.

The main function is `CloudFormationDeploy` which takes a yaml string and returns an error type. It deploys a cloudformation template to AWS, waiting for the stack to finish updating for about 5 minutes. It simplifies the create-or-update semantics, and handles retries for status checking.
