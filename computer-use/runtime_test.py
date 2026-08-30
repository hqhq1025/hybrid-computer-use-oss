#!/usr/bin/env python3
"""Compatibility entrypoint for the split Linux provider test suite."""

import unittest

from runtime_tests.test_core import *  # noqa: F401,F403
from runtime_tests.test_gui import *  # noqa: F401,F403
from runtime_tests.test_protocol import *  # noqa: F401,F403
from runtime_tests.test_inventory_resolver import *  # noqa: F401,F403
from runtime_tests.test_semantic_resolver import *  # noqa: F401,F403
from runtime_tests.test_snapshot import *  # noqa: F401,F403


if __name__ == "__main__":
    unittest.main(verbosity=2)
