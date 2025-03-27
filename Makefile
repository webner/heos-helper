.PHONY: deploy

deploy:
	docker buildx build --platform linux/amd64 -t registry.ebner.dev/heos-helper:latest --push . 