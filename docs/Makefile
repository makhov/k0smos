include Makefile.variables

# mkdocs.yml cannot live in docs/, because MkDocs rejects a config file whose
# docs_dir is its own parent. Hence the cd.
.PHONY: docs
docs: .require-mkdocs
	cd .. && PYTHONPATH="$(CURDIR)" mkdocs build --strict

.PHONY: serve
serve: .require-mkdocs
	cd .. && PYTHONPATH="$(CURDIR)" mkdocs serve

.PHONY: .require-mkdocs
.require-mkdocs:
	@which mkdocs >/dev/null 2>/dev/null || { \
	  echo 'mkdocs required, use pip install --disable-pip-version-check -r $(CURDIR)/requirements.txt' >&2; \
	  exit 1; \
	}

.docker-image.serve-dev.stamp: Dockerfile.serve-dev requirements_pip.txt requirements.txt Makefile.variables
	docker build \
	  --build-arg PYTHON_IMAGE_VERSION=$(python_version)-alpine$(alpine_version) \
	  -t 'k0smosdocs$(basename $@)' -f '$<' .
	touch -- '$@'

# Serve the site from the container, so no local Python is needed.
# /k0smos here must match WORKDIR and PYTHONPATH in
# docs/Dockerfile.serve-dev — rename one, rename both, or mkdocs fails
# at startup with ModuleNotFoundError.
.PHONY: serve-dev
serve-dev: .docker-image.serve-dev.stamp
	docker run --rm -it -p 8000:8000 -v '$(CURDIR)/..:/k0smos' k0smosdocs.docker-image.serve-dev

.PHONY: update-pip-requirements
update-pip-requirements: .docker-image.serve-dev.stamp
	docker run --rm --entrypoint sh k0smosdocs.docker-image.serve-dev -c \
	'pip install --disable-pip-version-check pip-tools > /dev/null \
	  && echo pip | pip-compile --allow-unsafe --output-file - - | grep -E -v "^ *#" \
	  && echo wheel | pip-compile --allow-unsafe --output-file - - | grep -E -v "^ *#"' \
	  > requirements_pip.txt.tmp
	mv -- requirements_pip.txt.tmp requirements_pip.txt
