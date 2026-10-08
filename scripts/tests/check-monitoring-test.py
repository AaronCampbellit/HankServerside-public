#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import unittest
import sys

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location("monitoring", Path(__file__).parents[1] / "check-monitoring.py")
monitoring = importlib.util.module_from_spec(spec)
spec.loader.exec_module(monitoring)


class MonitoringTest(unittest.TestCase):
    def response(self, base, path, query=None):
        if path.endswith("rules"):
            return {"groups": [{"rules": [{"name": name, "health": "ok"} for name in monitoring.REQUIRED_RULES]}]}
        return {"resultType": "vector", "result": [{"value": [1, "1"]}]}

    def test_healthy(self):
        self.assertEqual(monitoring.check("http://fixture", self.response), [])

    def test_down_absent_and_unconfigured(self):
        for sample in ([], [{"value": [1, "0"]}], [{"value": [1, "NaN"]}]):
            def response(base, path, query=None):
                if query and ('hank-cloud' in query or 'alertmanager_integrations' in query):
                    return {"resultType": "vector", "result": sample}
                return self.response(base, path, query)
            failures = monitoring.check("http://fixture", response)
            self.assertIn("hank-cloud scrape is healthy", failures)
            self.assertIn("Alertmanager has an active delivery integration", failures)

    def test_rule_failures(self):
        def response(base, path, query=None):
            if path.endswith("rules"):
                return {"groups": [{"rules": [{"name": "old-rule", "health": "err"}]}]}
            return self.response(base, path, query)
        self.assertEqual(len(monitoring.check("http://fixture", response)), 2)

    def test_network_failure_is_not_success(self):
        def response(*args):
            raise OSError("sensitive-response-must-not-be-printed")
        failures = monitoring.check("http://fixture", response)
        self.assertEqual(len(failures), 9)
        self.assertNotIn("sensitive-response", str(failures))


if __name__ == "__main__":
    unittest.main()
