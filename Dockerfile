FROM centos
COPY heos-helper /
COPY config.yaml /
CMD ["/heos-helper"]
