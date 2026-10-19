#!/usr/bin/env python3
"""Run every Linux provider test module as a single unittest suite."""

import unittest

from runtime_tests.test_core import *  # noqa: F401,F403
from runtime_tests.test_gui import *  # noqa: F401,F403
from runtime_tests.test_protocol import *  # noqa: F401,F403
from runtime_tests.test_inventory_resolver import *  # noqa: F401,F403
from runtime_tests.test_semantic_resolver import *  # noqa: F401,F403
from runtime_tests.test_snapshot import *  # noqa: F401,F403
from runtime_tests.test_events import *  # noqa: F401,F403
from runtime_tests.test_capabilities import *  # noqa: F401,F403


if __name__ == "__main__":
    unittest.main(verbosity=2)
