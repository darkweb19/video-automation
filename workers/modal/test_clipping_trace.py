"""Privacy and observability regression tests for Modal stdout breadcrumbs."""

from contextlib import redirect_stdout
from io import StringIO
import unittest
from unittest.mock import patch

import clipping_m3_worker
from clipping_protocol import trace_worker


class ClippingTraceTests(unittest.TestCase):
    def test_flush_safe_scalar_breadcrumb_and_reject_sensitive_fields(self):
        output = StringIO()
        with redirect_stdout(output):
            trace_worker(
                "dispatch_gpu_spawn_submitted",
                job="clipjob_0123456789abcdef",
                attempt="clipatt_abcdef0123456789",
                call_id="fc-012345",
                phase="spawn",
                elapsed_ms=1234,
                secret="should-not-appear",
                media_url="https://private.example/video",
                reason="Bearer private-token cannot be printed",
            )
        event = output.getvalue().strip()
        self.assertTrue(event.startswith("framevault_trace event=dispatch_gpu_spawn_submitted"))
        self.assertIn("job=clipjob_0123456789abcdef", event)
        self.assertIn("attempt=clipatt_abcdef0123456789", event)
        self.assertIn("call_id=fc-012345", event)
        self.assertIn("elapsed_ms=1234", event)
        self.assertIn("reason=-", event)
        self.assertNotIn("should-not-appear", event)
        self.assertNotIn("private.example", event)
        self.assertNotIn("private-token", event)
        self.assertEqual(output.getvalue().count("\n"), 1)

    def test_invalid_events_and_string_injection_are_not_emitted(self):
        output = StringIO()
        with redirect_stdout(output):
            trace_worker("invalid\nleak", job="job")
            trace_worker("safe_marker", job="job\nBearer abc", phase="phase\nsecret")
        data = output.getvalue()
        self.assertEqual(len(data.splitlines()), 1)
        self.assertIn("job=-", data)
        self.assertIn("phase=-", data)
        self.assertNotIn("Bearer", data)
        self.assertNotIn("secret", data)

    def test_scheduled_prune_is_silent_but_state_changes_still_trace(self):
        output = StringIO()
        with patch.object(clipping_m3_worker, "_MODAL_LEDGER_VOLUME", object()), \
             patch.object(clipping_m3_worker, "run_ledger_rpc", return_value={"pruned": True}) as rpc, \
             redirect_stdout(output):
            self.assertEqual(clipping_m3_worker.clipping_ledger_owner(), {"pruned": True})
            self.assertEqual(clipping_m3_worker.clipping_ledger_owner("claim", {}), {"pruned": True})
        self.assertNotIn("operation=prune", output.getvalue())
        self.assertIn("framevault_trace event=ledger_owner_entered", output.getvalue())
        self.assertIn("operation=claim", output.getvalue())
        self.assertEqual(rpc.call_count, 2)


if __name__ == "__main__":
    unittest.main()
