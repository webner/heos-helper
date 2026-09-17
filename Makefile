IMAGE_REPO ?= registry.int.ebner.dev/heos-helper
IMAGE_TAG ?= $(shell git rev-parse --short HEAD)
IMAGE ?= $(IMAGE_REPO):$(IMAGE_TAG)-amd64
KUBECTL ?= kubectl --context k3s-node01
MANIFEST ?= k8s/heos-helper.yaml

.PHONY: image deploy status logs

# The tag is the commit, so refuse to build from uncommitted changes.
image:
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty; commit first"; exit 1; }
	docker buildx build --platform linux/amd64 -t $(IMAGE) --push .

# The image is substituted before applying rather than set afterwards, so the
# Deployment never briefly points at the placeholder tag in the manifest.
deploy: image
	sed 's#image: $(IMAGE_REPO):latest#image: $(IMAGE)#' $(MANIFEST) | $(KUBECTL) apply -f -
	$(KUBECTL) -n default rollout status deployment/heos-helper --timeout=120s

status:
	$(KUBECTL) -n default get pods,svc -l app=heos-helper -o wide

logs:
	$(KUBECTL) -n default logs deployment/heos-helper --tail=100 -f
