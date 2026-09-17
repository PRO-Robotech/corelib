"""OS-boundary peer for interruption/malformed-output probes, not a checker."""
import signal
import sys
sys.stdin.buffer.read()
if sys.argv[1] == "stall-checker":
    signal.pause()
else:
    print('{"synthetic-np-upload-apricot":')
