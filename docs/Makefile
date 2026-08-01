include Makefile.variables

# mkdocs.yml cannot live in docs/, because MkDocs rejects a config file whose
# docs_dir is its own parent. Hence the cd.
.PHONY: docs
docs: .require-mkdocs
	cd .. && mkdocs build --strict

.PHONY: serve
serve: .require-mkdocs
	cd .. && mkdocs serve

.PHONY: .require-mkdocs
.require-mkdocs:
	@which mkdocs >/dev/null 2>/dev/null || { \
	  echo 'mkdocs required, use pip install --disable-pip-version-check -r $(CURDIR)/requirements.txt' >&2; \
	  exit 1; \
	}
