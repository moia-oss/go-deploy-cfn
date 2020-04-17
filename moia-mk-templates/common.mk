# This make target makes environment variables mandatory

ssm-get = $(shell aws ssm get-parameter --name '$(1)' --with-decryption --region '$(AWS_REGION)' --query 'Parameter.Value' --output text)

# To properly use this, you need to add guard-{YOUR_ENV_VAR} as a dependency
# to your make target.
# Example:
# Consider you want to make MOIA_ENVIRONMENT mandatory for your deploy make 
# target. You then need to add the following line to your deploy target:
# deploy: guard-MOIA_ENVIRONMENT
# 	...
#
# There is also a special case for MNOIA_ENVIRONMENT. If we have a kubernetes
# context, we check if the name of environment in the cluster name is the same
# otherwise we abort as well, because the wrong env will probably be applied in
# the wrong cluster
guard-%:
	@if [ $* = "MOIA_ENVIRONMENT" ]; then \
		if [ -x "$$(command -v kubectl)" ]; then \
			cluster="$$(kubectl cluster-info | head -n1 | awk '{print $$NF}' | sed $$'s,\x1b\\[[0-9;]*[a-zA-Z],,g')"; \
			env="$$(echo "$$cluster" | perl -n -e '/^https:\/\/api\.cluster\.trip\.(\w+)\.moia\-group\.io$$/ && print $$1')"; \
			if [ "$$env" = "poc" ] || [ "$$env" = "dev" ] || [ "$$env" = "int" ] || [ "$$env" = "prd" ]; then \
				if [ "$$env" != "${${*}}" ]; then \
					echo "Cluster name is $$cluster, but MOIA_ENVIRONMENT is $$MOIA_ENVIRONMENT. Aborting..."; \
					exit 1; \
				fi \
			fi \
		fi \
	fi; \
	if [ "${${*}}" = "" ]; then \
		echo "Environment variable $* not set"; \
		exit 1; \
	fi
