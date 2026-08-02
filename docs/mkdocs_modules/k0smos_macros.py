"""Macros available to the documentation.

Values are exported as macros rather than variables on purpose. An undefined
variable renders as empty text and passes a --strict build, so a typo in a
version reference ships silently; an undefined macro raises and fails the
build. k0s does the same thing for the same reason.
"""

import os
import subprocess


def define_env(env):
    @env.macro
    def k0smos_version():
        """The release this documentation describes.

        K0SMOS_VERSION is set by CI, which knows whether it is building `head`
        or a tagged release. Locally it falls back to the most recent tag, so a
        working copy renders something real rather than a placeholder.
        """
        version = os.getenv("K0SMOS_VERSION")
        if version:
            return version
        try:
            return subprocess.check_output(
                ["git", "describe", "--tags", "--abbrev=0"],
                stderr=subprocess.DEVNULL,
                text=True,
            ).strip()
        except (subprocess.CalledProcessError, FileNotFoundError):
            # A source tarball has no git history. Better a visible placeholder
            # than a build that fails for someone who only wants to read.
            return "latest"
