SELF_DIR := $(dir $(lastword $(MAKEFILE_LIST)))
include $(SELF_DIR)/common.mk

DOCKER_REGISTRY ?= 614608043005.dkr.ecr.eu-central-1.amazonaws.com

.PHONY: docker-build
docker-build: guard-SERVICE guard-DOCKER_REGISTRY
	docker build --no-cache -t $(DOCKER_REGISTRY)/$(SERVICE) .

.PHONY: push-image
push-image: guard-SERVICE guard-DOCKER_REGISTRY docker-build
	aws ecr get-login-password | docker login --username AWS --password-stdin $(DOCKER_REGISTRY)
	docker push $(DOCKER_REGISTRY)/$(SERVICE)
